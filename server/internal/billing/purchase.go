package billing

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/subscription"
)

// Wallet plan purchases (docs/contracts/phase15-api.md §2–§3): buy a plan, renew
// it, or upgrade a live subscription to a more expensive plan by paying the
// prorated difference. Lock order: subscription.LockUser → plans →
// subscriptions → wallets.

// RefPurchase is the ledger reference type of purchase debits.
const RefPurchase = "purchase"

// PurchaseOptions is GET /api/billing/purchase/options.
type PurchaseOptions struct {
	Available money.Amount
	Currency  string
	Plans     []PurchaseOption
	// Active are the user's live subscriptions (the store warns when a purchase
	// adds a parallel subscription next to one covering the same models).
	Active []*subscription.Subscription
}

// PurchaseOption describes what buying one plan would do now.
type PurchaseOption struct {
	Plan                *subscription.Plan
	Purchasable         bool
	Action              string // new | renew
	Price               *money.Amount
	RenewSubscriptionID *uuid.UUID
	CurrentEndsAt       *time.Time
	NewEndsAt           *time.Time
	Upgrades            []UpgradeOption
}

// UpgradeOption is one live subscription that can be upgraded to the plan.
type UpgradeOption struct {
	FromSubscriptionID uuid.UUID
	FromPlanID         uuid.UUID
	FromPlanName       string
	FromPrice          *money.Amount
	Price              money.Amount
	// Credit is the value of the unused old time deducted from the price.
	Credit    money.Amount
	Remaining time.Duration
	EndsAt    time.Time
}

// PurchaseOptions lists the active plans with the price and effect of buying,
// renewing or upgrading to each of them for userID.
func (s *Service) PurchaseOptions(ctx context.Context, userID uuid.UUID) (*PurchaseOptions, error) {
	now := s.now()
	w, err := s.GetWallet(ctx, userID)
	if err != nil {
		return nil, err
	}
	plans, err := subscription.ActivePlansTx(ctx, s.pool, now)
	if err != nil {
		return nil, err
	}
	subs, err := subscription.LiveSubscriptionsTx(ctx, s.pool, userID, now)
	if err != nil {
		return nil, err
	}
	fromPlans := map[uuid.UUID]*subscription.Plan{}
	for _, sub := range subs {
		if _, ok := fromPlans[sub.PlanID]; ok {
			continue
		}
		p, err := subscription.PlanTx(ctx, s.pool, sub.PlanID, now, false)
		if err != nil {
			return nil, err
		}
		fromPlans[sub.PlanID] = p
	}
	out := &PurchaseOptions{Available: w.Available(), Currency: w.Currency, Plans: []PurchaseOption{}, Active: subs}
	for _, p := range plans {
		o := PurchaseOption{Plan: p, Purchasable: p.Purchasable(), Action: subscription.PurchaseNew, Upgrades: []UpgradeOption{}}
		period, err := p.Period()
		if err != nil {
			return nil, err
		}
		if o.Purchasable {
			o.Price = p.ListPrice
			ends := now.Add(period)
			if t := subscription.RenewTarget(subs, p); t != nil {
				o.Action = subscription.PurchaseRenew
				o.RenewSubscriptionID, o.CurrentEndsAt = &t.ID, &t.EndsAt
				ends = t.EndsAt.Add(period)
			}
			o.NewEndsAt = &ends
			for _, sub := range subs {
				if sub.PlanID == p.ID {
					continue
				}
				from := fromPlans[sub.PlanID]
				q, err := subscription.UpgradeCheck(sub, from, p, subs, now)
				if err != nil {
					continue
				}
				u := UpgradeOption{FromSubscriptionID: sub.ID, FromPlanID: sub.PlanID, FromPlanName: sub.PlanName,
					Price: q.Price, Credit: q.Credit, Remaining: sub.EndsAt.Sub(now), EndsAt: q.EndsAt}
				if from != nil {
					u.FromPrice = from.ListPrice
				}
				o.Upgrades = append(o.Upgrades, u)
			}
			sort.SliceStable(o.Upgrades, func(i, j int) bool { return o.Upgrades[i].Price < o.Upgrades[j].Price })
		}
		out.Plans = append(out.Plans, o)
	}
	return out, nil
}

// PurchaseInput is POST /api/billing/purchase.
type PurchaseInput struct {
	PlanID             uuid.UUID
	FromSubscriptionID *uuid.UUID
	// ExpectedPrice is the price the user was shown; a higher actual price is
	// rejected with price_changed.
	ExpectedPrice money.Amount
}

// PurchaseResult is the outcome of a purchase.
type PurchaseResult struct {
	ID           uuid.UUID
	Action       string
	Price        money.Amount
	Wallet       Wallet
	Subscription *subscription.SubscriptionView
}

// Purchase charges the wallet and grants, renews or upgrades the plan in one
// transaction.
func (s *Service) Purchase(ctx context.Context, actor Actor, in PurchaseInput, meta RequestMeta) (*PurchaseResult, error) {
	currency := s.Currency(ctx)
	var res PurchaseResult
	var w *walletRow
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		w = nil
		now := s.now()
		if err := subscription.LockUser(ctx, tx, actor.ID); err != nil {
			return err
		}
		plan, err := subscription.PlanTx(ctx, tx, in.PlanID, now, true)
		if err != nil {
			return err
		}
		if plan.Status != subscription.PlanActive {
			return subscription.PlanArchivedError()
		}
		if !plan.Purchasable() {
			return subscription.PlanNotForSaleError()
		}
		subs, err := subscription.LiveSubscriptionsTx(ctx, tx, actor.ID, now)
		if err != nil {
			return err
		}
		action, price := subscription.PurchaseNew, *plan.ListPrice
		var from *subscription.Subscription
		var fromPlan *subscription.Plan
		var quote subscription.UpgradeQuote
		if in.FromSubscriptionID != nil {
			if from, err = subscription.GetSubTx(ctx, tx, *in.FromSubscriptionID, true); err != nil {
				return err
			}
			if from.UserID != actor.ID {
				return apperr.NotFound("订阅")
			}
			if fromPlan, err = subscription.PlanTx(ctx, tx, from.PlanID, now, false); err != nil {
				return err
			}
			if quote, err = subscription.UpgradeCheck(from, fromPlan, plan, subs, now); err != nil {
				return err
			}
			price = quote.Price
			action = subscription.PurchaseUpgrade
		} else if subscription.RenewTarget(subs, plan) != nil {
			action = subscription.PurchaseRenew
		}
		if price > in.ExpectedPrice {
			e := apperr.New(apperr.KindConflict, "price_changed", "价格已变化，请确认新价格后重试")
			e.Details = map[string]any{"price": price.String()}
			return e
		}
		if w, err = s.lockWallet(ctx, tx, actor.ID, true); err != nil {
			return err
		}
		if avail := w.balance - w.reserved; avail < price {
			e := InsufficientBalance()
			e.Details = map[string]any{"price": price.String(), "available": avail.String()}
			return e
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		var sub *subscription.Subscription
		var note string
		switch action {
		case subscription.PurchaseUpgrade:
			note = "升级套餐 " + from.PlanName + " → " + plan.Name
		case subscription.PurchaseRenew:
			note = "续费套餐 " + plan.Name
		default:
			note = "购买套餐 " + plan.Name
		}
		entry, err := s.applyLedger(ctx, tx, w, KindCharge, -price, RefPurchase, id.String(), &note, nil)
		if err != nil {
			return err
		}
		if action == subscription.PurchaseUpgrade {
			sub, err = subscription.UpgradeTx(ctx, tx, now, from, plan, quote.EndsAt)
		} else {
			var renewed bool
			sub, renewed, err = subscription.GrantTx(ctx, tx, now, actor.ID, plan.ID, 1, subscription.SourcePurchase, id.String())
			if renewed {
				action = subscription.PurchaseRenew
			}
		}
		if err != nil {
			return err
		}
		var fromPlanID *uuid.UUID
		var fromPlanName *string
		if from != nil {
			fromPlanID, fromPlanName = &from.PlanID, &from.PlanName
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plan_purchases (id, user_id, action, plan_id, plan_name, subscription_id,
				from_plan_id, from_plan_name, price_nano, ledger_entry_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			id, actor.ID, action, plan.ID, plan.Name, sub.ID, fromPlanID, fromPlanName, int64(price), entry.ID, now); err != nil {
			return err
		}
		view, err := subscription.DescribeTx(ctx, tx, sub, now)
		if err != nil {
			return err
		}
		res = PurchaseResult{ID: id, Action: action, Price: price, Wallet: toWallet(w, actor.ID, currency), Subscription: view}
		md := map[string]any{"action": action, "planId": plan.ID, "planName": plan.Name, "price": price.String(),
			"subscriptionId": sub.ID, "endsAt": sub.EndsAt, "purchaseId": id, "ledgerEntryId": entry.ID}
		if from != nil {
			md["fromPlanId"], md["fromPlanName"] = from.PlanID, from.PlanName
		}
		return s.audit(ctx, tx, actor, meta, subscription.ActionPurchase, "subscription", sub.ID.String(), md)
	})
	if err != nil {
		return nil, err
	}
	s.dispatch(ctx, w)
	return &res, nil
}

// PurchaseRecord is one row of GET /api/billing/purchases.
type PurchaseRecord struct {
	ID             uuid.UUID
	Action         string
	PlanID         uuid.UUID
	PlanName       string
	FromPlanName   *string
	Price          money.Amount
	SubscriptionID *uuid.UUID
	CreatedAt      time.Time
}

// ListPurchases returns the user's purchases newest first.
func (s *Service) ListPurchases(ctx context.Context, userID uuid.UUID, offset, limit int) ([]PurchaseRecord, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM plan_purchases WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, action, plan_id, plan_name, from_plan_name, price_nano, subscription_id, created_at
		FROM plan_purchases WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []PurchaseRecord{}
	for rows.Next() {
		var r PurchaseRecord
		var price int64
		if err := rows.Scan(&r.ID, &r.Action, &r.PlanID, &r.PlanName, &r.FromPlanName, &price, &r.SubscriptionID, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		r.Price, r.CreatedAt = money.Amount(price), r.CreatedAt.UTC()
		out = append(out, r)
	}
	return out, total, rows.Err()
}
