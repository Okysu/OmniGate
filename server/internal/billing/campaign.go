package billing

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"omnigate/internal/apperr"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/subscription"
)

type CampaignConditions = subscription.AudienceFilter

type CampaignReward struct {
	Quantity  int         `json:"quantity"`
	Kind      string      `json:"kind,omitempty"`
	ValidDays int         `json:"validDays,omitempty"`
	PlanIDs   []uuid.UUID `json:"planIds,omitempty"`
}

type CampaignRewardItem struct {
	Type      string      `json:"type"`
	PlanID    *uuid.UUID  `json:"planId,omitempty"`
	Amount    string      `json:"amount,omitempty"`
	Quantity  int         `json:"quantity,omitempty"`
	Kind      string      `json:"kind,omitempty"`
	ValidDays int         `json:"validDays,omitempty"`
	PlanIDs   []uuid.UUID `json:"planIds,omitempty"`
}

type Campaign struct {
	Rewards      []CampaignRewardItem `json:"rewards"`
	ID           uuid.UUID            `json:"id"`
	Title        string               `json:"title"`
	Description  string               `json:"description"`
	Status       string               `json:"status"`
	Reward       *CampaignReward      `json:"reward"`
	RewardType   string               `json:"rewardType"`
	PlanID       *uuid.UUID           `json:"planId"`
	Amount       string               `json:"amount"`
	Price        string               `json:"price"`
	Stock        int64                `json:"stock"`
	PerUserLimit int                  `json:"perUserLimit"`
	StartsAt     *time.Time           `json:"startsAt"`
	EndsAt       *time.Time           `json:"endsAt"`
	Conditions   CampaignConditions   `json:"conditions"`
	Version      int                  `json:"version"`
	CreatedAt    time.Time            `json:"createdAt"`
	UpdatedAt    time.Time            `json:"updatedAt"`
}

const campaignCols = `id,title,description,status,reward_type,plan_id,amount_nano,price_nano,stock,per_user_limit,starts_at,ends_at,conditions,version,created_at,updated_at,reward,rewards`

func scanCampaign(row db.Row) (*Campaign, error) {
	var c Campaign
	var amount, price int64
	var raw, reward, rewards string
	err := row.Scan(&c.ID, &c.Title, &c.Description, &c.Status, &c.RewardType, &c.PlanID, &amount, &price, &c.Stock, &c.PerUserLimit, &c.StartsAt, &c.EndsAt, &raw, &c.Version, &c.CreatedAt, &c.UpdatedAt, &reward, &rewards)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &c.Conditions); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(reward), &c.Reward); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(rewards), &c.Rewards); err != nil {
		return nil, err
	}
	if c.Conditions.GroupIDs == nil {
		c.Conditions.GroupIDs = []uuid.UUID{}
	}
	if c.Conditions.UserIDs == nil {
		c.Conditions.UserIDs = []uuid.UUID{}
	}
	c.Amount = money.Amount(amount).String()
	c.Price = money.Amount(price).String()
	return &c, nil
}

func campaignConflict(code, msg string) error { return apperr.New(apperr.KindConflict, code, msg) }

func (s *Service) SaveCampaign(ctx context.Context, actor Actor, id uuid.UUID, in Campaign, meta RequestMeta) (*Campaign, error) {
	if err := prepareRewards(&in); err != nil {
		return nil, err
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	if in.Amount == "" {
		in.Amount = "0"
	}
	if in.Price == "" {
		in.Price = "0"
	}
	if in.Conditions.MinSpend == "" {
		in.Conditions.MinSpend = "0"
	}
	amount, e1 := money.Parse(in.Amount)
	price, e2 := money.Parse(in.Price)
	spend, e3 := money.Parse(in.Conditions.MinSpend)
	if in.Title == "" || utf8.RuneCountInString(in.Title) > 100 || utf8.RuneCountInString(in.Description) > 10000 || in.Stock < -1 || in.PerUserLimit < 1 || in.PerUserLimit > 1000 || e1 != nil || e2 != nil || e3 != nil || amount < 0 || price < 0 || spend < 0 {
		return nil, apperr.Validation("请检查活动标题、库存、领取次数和金额", nil)
	}
	if in.Status != "draft" && in.Status != "active" && in.Status != "archived" {
		return nil, apperr.Validation("活动状态无效", nil)
	}
	if err := in.Conditions.Validate(); err != nil {
		return nil, err
	}
	in.Amount = amount.String()
	for i := range in.Rewards {
		if in.Rewards[i].Amount != "" {
			v, _ := money.Parse(in.Rewards[i].Amount)
			in.Rewards[i].Amount = v.String()
		}
	}
	hasSubscription := false
	seenPlans := map[uuid.UUID]bool{}
	for _, r := range in.rewardItems() {
		if err := validateRewardItem(r); err != nil {
			return nil, err
		}
		if r.Type == "subscription" {
			hasSubscription = true
			if seenPlans[*r.PlanID] {
				return nil, apperr.Validation("同一活动不能重复配置同一套餐", nil)
			}
			seenPlans[*r.PlanID] = true
		}
	}
	if !hasSubscription && price != 0 {
		return nil, apperr.Validation("未包含套餐的活动必须免费领取", nil)
	}
	if in.StartsAt != nil && in.EndsAt != nil && !in.StartsAt.Before(*in.EndsAt) {
		return nil, apperr.Validation("活动结束时间必须晚于开始时间", nil)
	}
	var out *Campaign
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		now := s.now()
		if err := subscription.ValidateAudienceReferences(ctx, tx, in.Conditions); err != nil {
			return err
		}
		create := id == uuid.Nil
		if !create {
			old, err := scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignCols+` FROM benefit_campaigns WHERE id=$1 FOR UPDATE`, id))
			if db.IsNoRows(err) {
				return apperr.NotFound("活动")
			}
			if err != nil {
				return err
			}
			if old.Version != in.Version {
				return apperr.VersionConflict()
			}
			var claims int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM benefit_claims WHERE campaign_id=$1`, id).Scan(&claims); err != nil {
				return err
			}
			if claims > 0 && (old.RewardType != in.RewardType || old.Amount != amount.String() || old.Price != price.String() || !samePlan(old.PlanID, in.PlanID) || !sameReward(old.Reward, in.Reward) || !sameRewardItems(old.rewardItems(), in.rewardItems())) {
				return apperr.Validation("已有领取记录，不能修改奖励或价格；请新建活动", nil)
			}
		}
		for _, r := range in.rewardItems() {
			if r.Type == "subscription" {
				p, err := subscription.PlanTx(ctx, tx, *r.PlanID, now, false)
				if err != nil {
					return err
				}
				if !p.Hidden || !p.Stackable || p.Status != subscription.PlanActive {
					return apperr.Validation("活动套餐必须有效、隐藏且可叠加", nil)
				}
			}
			for _, pid := range r.PlanIDs {
				if _, err := subscription.PlanTx(ctx, tx, pid, now, false); err != nil {
					return err
				}
			}
		}
		raw, _ := json.Marshal(in.Conditions)
		reward, _ := json.Marshal(in.Reward)
		rewards, _ := json.Marshal(in.Rewards)
		if create {
			id = uuid.Must(uuid.NewV7())
			_, err := tx.Exec(ctx, `INSERT INTO benefit_campaigns (`+campaignCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,1,$14,$14,$15,$16)`, id, in.Title, in.Description, in.Status, in.RewardType, in.PlanID, int64(amount), int64(price), in.Stock, in.PerUserLimit, in.StartsAt, in.EndsAt, string(raw), now, string(reward), string(rewards))
			if err != nil {
				return err
			}
		} else {
			_, err := tx.Exec(ctx, `UPDATE benefit_campaigns SET title=$2,description=$3,status=$4,reward_type=$5,plan_id=$6,amount_nano=$7,price_nano=$8,stock=$9,per_user_limit=$10,starts_at=$11,ends_at=$12,conditions=$13,version=version+1,updated_at=$14,reward=$15,rewards=$16 WHERE id=$1`, id, in.Title, in.Description, in.Status, in.RewardType, in.PlanID, int64(amount), int64(price), in.Stock, in.PerUserLimit, in.StartsAt, in.EndsAt, string(raw), now, string(reward), string(rewards))
			if err != nil {
				return err
			}
		}
		var err error
		out, err = scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignCols+` FROM benefit_campaigns WHERE id=$1`, id))
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, "benefit.save", "campaign", id.String(), map[string]any{"title": in.Title, "status": in.Status, "stock": in.Stock, "version": out.Version, "conditions": in.Conditions})
	})
	return out, err
}

func sameReward(a, b *CampaignReward) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func samePlan(a, b *uuid.UUID) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (s *Service) Campaigns(ctx context.Context, admin bool) ([]*Campaign, error) {
	where := " WHERE status='active'"
	if admin {
		where = ""
	}
	rows, err := s.pool.Query(ctx, `SELECT `+campaignCols+` FROM benefit_campaigns`+where+` ORDER BY created_at DESC,id DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type CampaignRewardGrant struct {
	Type             string     `json:"type"`
	Quantity         int        `json:"quantity,omitempty"`
	SubscriptionID   *uuid.UUID `json:"subscriptionId,omitempty"`
	ResetCardBatchID *uuid.UUID `json:"resetCardBatchId,omitempty"`
	LedgerID         *uuid.UUID `json:"ledgerId,omitempty"`
}
type CampaignClaim struct {
	Rewards          []CampaignRewardGrant `json:"rewards"`
	ResetCardBatchID *uuid.UUID            `json:"resetCardBatchId"`
	LotteryChances   int                   `json:"lotteryChances"`
	ID               uuid.UUID             `json:"id"`
	CampaignID       uuid.UUID             `json:"campaignId"`
	SubscriptionID   *uuid.UUID            `json:"subscriptionId"`
	LedgerID         *uuid.UUID            `json:"ledgerId"`
	CreatedAt        time.Time             `json:"createdAt"`
}

func claimByKey(ctx context.Context, q db.Querier, user, key uuid.UUID) (*CampaignClaim, error) {
	var c CampaignClaim
	var raw string
	err := q.QueryRow(ctx, `SELECT id,campaign_id,subscription_id,ledger_id,created_at,reset_card_batch_id,lottery_chances,reward_results FROM benefit_claims WHERE user_id=$1 AND request_id=$2`, user, key).Scan(&c.ID, &c.CampaignID, &c.SubscriptionID, &c.LedgerID, &c.CreatedAt, &c.ResetCardBatchID, &c.LotteryChances, &raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c.Rewards)
	}
	return &c, err
}

func (s *Service) checkCampaign(ctx context.Context, q db.Querier, c *Campaign, user uuid.UUID) error {
	users, _, err := subscription.AudienceUsers(ctx, q, c.Conditions, s.now(), []uuid.UUID{user})
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return campaignConflict("campaign_not_eligible", "不符合本活动的领取条件")
	}
	return nil
}

func (s *Service) ClaimCampaign(ctx context.Context, actor Actor, id, key uuid.UUID, expected money.Amount, meta RequestMeta) (*CampaignClaim, error) {
	if key == uuid.Nil {
		return nil, apperr.Validation("缺少领取请求编号", nil)
	}
	var out *CampaignClaim
	var wallet *walletRow
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		out = nil
		wallet = nil
		now := s.now()
		if err := subscription.LockUser(ctx, tx, actor.ID); err != nil {
			return err
		}
		old, err := claimByKey(ctx, tx, actor.ID, key)
		if err == nil {
			if old.CampaignID != id {
				return campaignConflict("request_reused", "请求编号已用于其他活动")
			}
			out = old
			return nil
		}
		if !db.IsNoRows(err) {
			return err
		}
		c, err := scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignCols+` FROM benefit_campaigns WHERE id=$1 FOR UPDATE`, id))
		if db.IsNoRows(err) {
			return apperr.NotFound("活动")
		}
		if err != nil {
			return err
		}
		if c.Status != "active" || c.StartsAt != nil && now.Before(*c.StartsAt) || c.EndsAt != nil && !now.Before(*c.EndsAt) {
			return campaignConflict("campaign_unavailable", "活动未开始或已结束")
		}
		if c.Stock == 0 {
			return campaignConflict("campaign_sold_out", "活动已领完")
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM benefit_claims WHERE campaign_id=$1 AND user_id=$2`, id, actor.ID).Scan(&count); err != nil {
			return err
		}
		if count >= c.PerUserLimit {
			return campaignConflict("campaign_limit", "已达到本活动的领取次数上限")
		}
		if err = s.checkCampaign(ctx, tx, c, actor.ID); err != nil {
			return err
		}
		price, _ := money.Parse(c.Price)
		if price > expected {
			return campaignConflict("price_changed", "活动价格已变化，请刷新后重试")
		}
		claimID := uuid.Must(uuid.NewV7())
		out = &CampaignClaim{ID: claimID, CampaignID: id, CreatedAt: now}
		items := c.rewardItems()
		plans := map[uuid.UUID]*subscription.Plan{}
		planIDs := []uuid.UUID{}
		balance := money.Amount(0)
		for _, r := range items {
			if r.Type == "subscription" {
				planIDs = append(planIDs, *r.PlanID)
			}
			if r.Type == "balance" {
				v, _ := money.Parse(r.Amount)
				balance, err = balance.Add(v)
				if err != nil {
					return err
				}
			}
		}
		// Stable lock order across bundle claims containing multiple plans.
		slices.SortFunc(planIDs, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		for _, pid := range planIDs {
			p, e := subscription.PlanTx(ctx, tx, pid, now, true)
			if e != nil {
				return e
			}
			if p.Status != subscription.PlanActive || !p.Hidden || !p.Stackable {
				return subscription.PlanArchivedError()
			}
			if err = subscription.ConsumeStockTx(ctx, tx, pid, 1); err != nil {
				return err
			}
			plans[pid] = p
		}
		var balanceLedger *uuid.UUID
		if price > 0 || balance > 0 {
			wallet, err = s.lockWallet(ctx, tx, actor.ID, true)
			if err != nil {
				return err
			}
			if wallet.balance-wallet.reserved < price {
				return InsufficientBalance()
			}
			note := "福利活动：" + c.Title
			if price > 0 {
				entry, e := s.applyLedger(ctx, tx, wallet, KindCharge, -price, RefPurchase, claimID.String(), &note, nil)
				if e != nil {
					return e
				}
				out.LedgerID = &entry.ID
			}
			if balance > 0 {
				entry, e := s.applyLedger(ctx, tx, wallet, KindGrant, balance, RefAdmin, claimID.String(), &note, nil)
				if e != nil {
					return e
				}
				balanceLedger = &entry.ID
				if out.LedgerID == nil {
					out.LedgerID = balanceLedger
				}
			}
		}
		purchaseIndex := 0
		for _, r := range items {
			grant := CampaignRewardGrant{Type: r.Type, Quantity: r.Quantity}
			switch r.Type {
			case "balance":
				grant.LedgerID = balanceLedger
			case "subscription":
				p := plans[*r.PlanID]
				source := subscription.SourceAdmin
				if price > 0 {
					source = subscription.SourcePurchase
				}
				sub, _, e := subscription.GrantTx(ctx, tx, now, actor.ID, p.ID, 1, source, claimID.String())
				if e != nil {
					return e
				}
				grant.SubscriptionID = &sub.ID
				if out.SubscriptionID == nil {
					out.SubscriptionID = &sub.ID
				}
				if price > 0 {
					// Attribute the activity price once across its subscriptions. The
					// remainder goes to the first item; the sum equals the charged price.
					allocated := price / money.Amount(len(planIDs))
					if purchaseIndex == 0 {
						allocated += price % money.Amount(len(planIDs))
					}
					purchaseIndex++
					if allocated > 0 {
						_, e = tx.Exec(ctx, `INSERT INTO plan_purchases(id,user_id,action,plan_id,plan_name,subscription_id,price_nano,ledger_entry_id,created_at) VALUES($1,$2,'new',$3,$4,$5,$6,$7,$8)`, uuid.Must(uuid.NewV7()), actor.ID, p.ID, p.Name, sub.ID, int64(allocated), out.LedgerID, now)
						if e != nil {
							return e
						}
					}
				}
			case "reset_card":
				var expires *time.Time
				if r.ValidDays > 0 {
					v := now.Add(time.Duration(r.ValidDays) * 24 * time.Hour)
					expires = &v
				}
				batch, e := subscription.IssueCardsTx(ctx, tx, now, actor.ID, subscription.IssueCardsInput{Kind: r.Kind, Quantity: r.Quantity, ExpiresAt: expires, PlanIDs: r.PlanIDs, Note: "福利活动：" + c.Title, Target: subscription.CardTarget{Type: subscription.CardTargetUsers, UserIDs: []uuid.UUID{actor.ID}}}, []uuid.UUID{actor.ID})
				if e != nil {
					return e
				}
				grant.ResetCardBatchID = &batch
				if out.ResetCardBatchID == nil {
					out.ResetCardBatchID = &batch
				}
			case "lottery":
				out.LotteryChances += r.Quantity
			}
			out.Rewards = append(out.Rewards, grant)
		}
		if c.Stock > 0 {
			if _, err = tx.Exec(ctx, `UPDATE benefit_campaigns SET stock=stock-1,version=version+1,updated_at=$2 WHERE id=$1`, id, now); err != nil {
				return err
			}
		}
		results, _ := json.Marshal(out.Rewards)
		if _, err = tx.Exec(ctx, `INSERT INTO benefit_claims(id,campaign_id,user_id,request_id,subscription_id,ledger_id,created_at,reset_card_batch_id,lottery_chances,reward_results) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, claimID, id, actor.ID, key, out.SubscriptionID, out.LedgerID, now, out.ResetCardBatchID, out.LotteryChances, string(results)); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, "benefit.claim", "campaign", id.String(), map[string]any{"claimId": claimID, "price": c.Price, "rewardType": c.RewardType, "amount": c.Amount, "planId": c.PlanID, "subscriptionId": out.SubscriptionID, "resetCardBatchId": out.ResetCardBatchID, "lotteryChances": out.LotteryChances, "rewards": out.Rewards})
	})
	if err == nil && wallet != nil {
		s.dispatch(ctx, wallet)
	}
	return out, err
}
