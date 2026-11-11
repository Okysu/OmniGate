package billing

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"omnigate/internal/apperr"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/subscription"
)

type CampaignConditions struct {
	GroupIDs         []uuid.UUID `json:"groupIds"`
	UserIDs          []uuid.UUID `json:"userIds"`
	RegisteredAfter  *time.Time  `json:"registeredAfter"`
	RegisteredBefore *time.Time  `json:"registeredBefore"`
	MinSpend         string      `json:"minSpend"`
	RequirePurchase  bool        `json:"requirePurchase"`
}

type Campaign struct {
	ID           uuid.UUID          `json:"id"`
	Title        string             `json:"title"`
	Description  string             `json:"description"`
	Status       string             `json:"status"`
	RewardType   string             `json:"rewardType"`
	PlanID       *uuid.UUID         `json:"planId"`
	Amount       string             `json:"amount"`
	Price        string             `json:"price"`
	Stock        int64              `json:"stock"`
	PerUserLimit int                `json:"perUserLimit"`
	StartsAt     *time.Time         `json:"startsAt"`
	EndsAt       *time.Time         `json:"endsAt"`
	Conditions   CampaignConditions `json:"conditions"`
	Version      int                `json:"version"`
	CreatedAt    time.Time          `json:"createdAt"`
	UpdatedAt    time.Time          `json:"updatedAt"`
}

const campaignCols = `id,title,description,status,reward_type,plan_id,amount_nano,price_nano,stock,per_user_limit,starts_at,ends_at,conditions,version,created_at,updated_at`

func scanCampaign(row db.Row) (*Campaign, error) {
	var c Campaign
	var amount, price int64
	var raw string
	err := row.Scan(&c.ID, &c.Title, &c.Description, &c.Status, &c.RewardType, &c.PlanID, &amount, &price, &c.Stock, &c.PerUserLimit, &c.StartsAt, &c.EndsAt, &raw, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &c.Conditions); err != nil {
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
	if in.RewardType != "balance" && in.RewardType != "subscription" {
		return nil, apperr.Validation("奖励类型无效", nil)
	}
	if in.RewardType == "balance" && (amount <= 0 || price != 0 || in.PlanID != nil) {
		return nil, apperr.Validation("余额活动必须免费，奖励金额大于零且不关联套餐", nil)
	}
	if in.RewardType == "subscription" && (in.PlanID == nil || amount != 0) {
		return nil, apperr.Validation("请选择活动套餐，套餐活动不能同时发放余额", nil)
	}
	if in.StartsAt != nil && in.EndsAt != nil && !in.StartsAt.Before(*in.EndsAt) {
		return nil, apperr.Validation("活动结束时间必须晚于开始时间", nil)
	}
	if in.Conditions.RegisteredAfter != nil && in.Conditions.RegisteredBefore != nil && !in.Conditions.RegisteredAfter.Before(*in.Conditions.RegisteredBefore) {
		return nil, apperr.Validation("注册时间范围无效", nil)
	}
	if len(in.Conditions.UserIDs) > 1000 || len(in.Conditions.GroupIDs) > 100 {
		return nil, apperr.Validation("条件名单过长", nil)
	}
	var out *Campaign
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		now := s.now()
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
			if claims > 0 && (old.RewardType != in.RewardType || old.Amount != amount.String() || old.Price != price.String() || !samePlan(old.PlanID, in.PlanID)) {
				return apperr.Validation("已有领取记录，不能修改奖励或价格；请新建活动", nil)
			}
		}
		if in.PlanID != nil {
			p, err := subscription.PlanTx(ctx, tx, *in.PlanID, now, false)
			if err != nil {
				return err
			}
			if !p.Hidden || !p.Stackable || p.Status != subscription.PlanActive {
				return apperr.Validation("活动必须关联有效、可叠加的隐藏套餐，以保证每次领取独立额度", nil)
			}
		}
		raw, _ := json.Marshal(in.Conditions)
		if create {
			id = uuid.Must(uuid.NewV7())
			_, err := tx.Exec(ctx, `INSERT INTO benefit_campaigns (`+campaignCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,1,$14,$14)`, id, in.Title, in.Description, in.Status, in.RewardType, in.PlanID, int64(amount), int64(price), in.Stock, in.PerUserLimit, in.StartsAt, in.EndsAt, string(raw), now)
			if err != nil {
				return err
			}
		} else {
			_, err := tx.Exec(ctx, `UPDATE benefit_campaigns SET title=$2,description=$3,status=$4,reward_type=$5,plan_id=$6,amount_nano=$7,price_nano=$8,stock=$9,per_user_limit=$10,starts_at=$11,ends_at=$12,conditions=$13,version=version+1,updated_at=$14 WHERE id=$1`, id, in.Title, in.Description, in.Status, in.RewardType, in.PlanID, int64(amount), int64(price), in.Stock, in.PerUserLimit, in.StartsAt, in.EndsAt, string(raw), now)
			if err != nil {
				return err
			}
		}
		var err error
		out, err = scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignCols+` FROM benefit_campaigns WHERE id=$1`, id))
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, "benefit.save", "campaign", id.String(), map[string]any{"title": in.Title, "status": in.Status, "stock": in.Stock, "version": out.Version})
	})
	return out, err
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

type CampaignClaim struct {
	ID             uuid.UUID  `json:"id"`
	CampaignID     uuid.UUID  `json:"campaignId"`
	SubscriptionID *uuid.UUID `json:"subscriptionId"`
	LedgerID       *uuid.UUID `json:"ledgerId"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func claimByKey(ctx context.Context, q db.Querier, user, key uuid.UUID) (*CampaignClaim, error) {
	var c CampaignClaim
	err := q.QueryRow(ctx, `SELECT id,campaign_id,subscription_id,ledger_id,created_at FROM benefit_claims WHERE user_id=$1 AND request_id=$2`, user, key).Scan(&c.ID, &c.CampaignID, &c.SubscriptionID, &c.LedgerID, &c.CreatedAt)
	return &c, err
}

func (s *Service) checkCampaign(ctx context.Context, q db.Querier, c *Campaign, user uuid.UUID) error {
	var group uuid.UUID
	var registered time.Time
	if err := q.QueryRow(ctx, `SELECT group_id,created_at FROM users WHERE id=$1`, user).Scan(&group, &registered); err != nil {
		return err
	}
	contains := func(ids []uuid.UUID, id uuid.UUID) bool {
		for _, v := range ids {
			if v == id {
				return true
			}
		}
		return false
	}
	fail := func() error { return campaignConflict("campaign_not_eligible", "不符合本活动的领取条件") }
	cond := c.Conditions
	if len(cond.GroupIDs) > 0 && !contains(cond.GroupIDs, group) || len(cond.UserIDs) > 0 && !contains(cond.UserIDs, user) {
		return fail()
	}
	if cond.RegisteredAfter != nil && registered.Before(*cond.RegisteredAfter) || cond.RegisteredBefore != nil && !registered.Before(*cond.RegisteredBefore) {
		return fail()
	}
	if cond.RequirePurchase {
		var count int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM plan_purchases WHERE user_id=$1 AND price_nano>0`, user).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fail()
		}
	}
	min, err := money.Parse(cond.MinSpend)
	if err != nil {
		return err
	}
	if min > 0 {
		var spent int64
		if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(spent_nano),0) FROM lottery_days WHERE user_id=$1`, user).Scan(&spent); err != nil {
			return err
		}
		if spent < int64(min) {
			return fail()
		}
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
		var plan *subscription.Plan
		if c.PlanID != nil {
			plan, err = subscription.PlanTx(ctx, tx, *c.PlanID, now, true)
			if err != nil {
				return err
			}
			if plan.Status != subscription.PlanActive || !plan.Hidden || !plan.Stackable {
				return subscription.PlanArchivedError()
			}
			if err = subscription.ConsumeStockTx(ctx, tx, plan.ID, 1); err != nil {
				return err
			}
		}
		if price > 0 || c.RewardType == "balance" {
			wallet, err = s.lockWallet(ctx, tx, actor.ID, true)
			if err != nil {
				return err
			}
			if wallet.balance-wallet.reserved < price {
				return InsufficientBalance()
			}
			kind, amount := KindCharge, -price
			if c.RewardType == "balance" {
				kind = KindGrant
				amount, _ = money.Parse(c.Amount)
			}
			note := "福利活动：" + c.Title
			refType := RefAdmin
			if price > 0 {
				refType = RefPurchase
			}
			entry, err := s.applyLedger(ctx, tx, wallet, kind, amount, refType, claimID.String(), &note, nil)
			if err != nil {
				return err
			}
			out.LedgerID = &entry.ID
		}
		if plan != nil {
			source := subscription.SourceAdmin
			if price > 0 {
				source = subscription.SourcePurchase
			}
			sub, _, err := subscription.GrantTx(ctx, tx, now, actor.ID, plan.ID, 1, source, claimID.String())
			if err != nil {
				return err
			}
			out.SubscriptionID = &sub.ID
			if price > 0 {
				if _, err = tx.Exec(ctx, `INSERT INTO plan_purchases(id,user_id,action,plan_id,plan_name,subscription_id,price_nano,ledger_entry_id,created_at) VALUES($1,$2,'new',$3,$4,$5,$6,$7,$8)`, claimID, actor.ID, plan.ID, plan.Name, sub.ID, int64(price), out.LedgerID, now); err != nil {
					return err
				}
			}
		}
		if c.Stock > 0 {
			if _, err = tx.Exec(ctx, `UPDATE benefit_campaigns SET stock=stock-1,version=version+1,updated_at=$2 WHERE id=$1`, id, now); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO benefit_claims(id,campaign_id,user_id,request_id,subscription_id,ledger_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, claimID, id, actor.ID, key, out.SubscriptionID, out.LedgerID, now); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, "benefit.claim", "campaign", id.String(), map[string]any{"claimId": claimID, "price": c.Price, "rewardType": c.RewardType, "amount": c.Amount, "planId": c.PlanID, "subscriptionId": out.SubscriptionID})
	})
	if err == nil && wallet != nil {
		s.dispatch(ctx, wallet)
	}
	return out, err
}
