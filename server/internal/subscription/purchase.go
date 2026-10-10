package subscription

import (
	"context"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

// Wallet purchases (docs/contracts/phase15-api.md): the billing package charges
// the wallet; this file holds the plan side — which plans are for sale, the
// upgrade price, and replacing a subscription's snapshot in place.

// ActionPurchase is the audit action of a wallet purchase.
const ActionPurchase = "subscription.purchase"

// Purchase actions.
const (
	PurchaseNew     = "new"
	PurchaseRenew   = "renew"
	PurchaseUpgrade = "upgrade"
)

// PriceDecimals is the precision purchase prices are rounded to.
const PriceDecimals = 2

// PlanNotForSaleError is returned when buying a plan without a price.
func PlanNotForSaleError() *apperr.Error {
	return apperr.New(apperr.KindConflict, "plan_not_for_sale", "该套餐不支持余额购买")
}

// NotAnUpgradeError is returned when an upgrade is not to a more expensive plan.
func NotAnUpgradeError(msg string) *apperr.Error {
	return apperr.New(apperr.KindConflict, "not_an_upgrade", msg)
}

// Purchasable reports whether p can be bought with the wallet.
func (p *Plan) Purchasable() bool {
	return p.Status == PlanActive && p.ListPrice != nil && *p.ListPrice > 0
}

// Period returns the plan's duration.
func (p *Plan) Period() (time.Duration, error) { return ParsePeriod(p.Duration) }

// PlanTx reads a plan through q; lock takes FOR SHARE (the lock GrantTx takes).
func PlanTx(ctx context.Context, q db.Querier, id uuid.UUID, now time.Time, lock bool) (*Plan, error) {
	if lock {
		var one int
		err := q.QueryRow(ctx, `SELECT 1 FROM plans WHERE id = $1 FOR SHARE`, id).Scan(&one)
		if db.IsNoRows(err) {
			return nil, apperr.NotFound("套餐")
		}
		if err != nil {
			return nil, err
		}
	}
	p, err := scanPlan(q.QueryRow(ctx, `SELECT `+planCols+` FROM plans p WHERE p.id = $2`, dbTime(now), id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("套餐")
	}
	return p, err
}

// ActivePlansTx returns the active plans in catalog order (newest first, as
// GET /api/plans).
func ActivePlansTx(ctx context.Context, q db.Querier, now time.Time) ([]*Plan, error) {
	rows, err := q.Query(ctx, `SELECT `+planCols+` FROM plans p WHERE p.status = 'active'
		ORDER BY p.created_at DESC, p.id DESC`, dbTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LiveSubscriptionsTx is LiveSubscriptions through q.
func LiveSubscriptionsTx(ctx context.Context, q db.Querier, userID uuid.UUID, now time.Time) ([]*Subscription, error) {
	rows, err := q.Query(ctx, `SELECT `+subCols+subFrom+`
		WHERE s.user_id = $1 AND s.status = 'active' AND s.ends_at > $2 AND s.starts_at <= $2
		ORDER BY s.ends_at, s.id`, userID, dbTime(now))
	if err != nil {
		return nil, err
	}
	return collectSubs(rows)
}

// GetSubTx reads a subscription through q (FOR UPDATE when lock).
func GetSubTx(ctx context.Context, q db.Querier, id uuid.UUID, lock bool) (*Subscription, error) {
	return getSub(ctx, q, id, lock)
}

// RenewTarget returns the user's live subscription of plan that a purchase of
// the (non-stackable) plan extends, or nil.
func RenewTarget(subs []*Subscription, plan *Plan) *Subscription {
	if plan.Stackable {
		return nil
	}
	var best *Subscription
	for _, s := range subs {
		if s.PlanID == plan.ID && (best == nil || s.EndsAt.After(best.EndsAt)) {
			best = s
		}
	}
	return best
}

// UpgradePrice is the price of switching a subscription from a plan priced
// oldPrice per oldPeriod to one priced newPrice per newPeriod for the remaining
// time: the difference of the two prorated prices, rounded up to
// PriceDecimals. A result ≤ 0 means the target is not an upgrade.
func UpgradePrice(newPrice money.Amount, newPeriod time.Duration, oldPrice money.Amount, oldPeriod time.Duration,
	remaining time.Duration) (money.Amount, error) {
	if remaining <= 0 || newPeriod <= 0 || oldPeriod <= 0 {
		return 0, nil
	}
	sec := int64(remaining / time.Second)
	n, err := newPrice.MulDiv(sec, int64(newPeriod/time.Second))
	if err != nil {
		return 0, err
	}
	o, err := oldPrice.MulDiv(sec, int64(oldPeriod/time.Second))
	if err != nil {
		return 0, err
	}
	diff := n - o
	if diff <= 0 {
		return 0, nil
	}
	return ceilAmount(diff, PriceDecimals), nil
}

// ceilAmount rounds a positive amount up to decimals.
func ceilAmount(a money.Amount, decimals int) money.Amount {
	step := money.Amount(1)
	for i := decimals; i < money.Scale; i++ {
		step *= 10
	}
	if r := a % step; r != 0 {
		a += step - r
	}
	return a
}

// UpgradeCheck validates an upgrade of from to plan for a user whose live
// subscriptions are subs, returning the price at now.
func UpgradeCheck(from *Subscription, fromPlan, plan *Plan, subs []*Subscription, now time.Time) (money.Amount, error) {
	if from.StatusAt(now) != StatusActive {
		return 0, NotActiveError()
	}
	if from.PlanID == plan.ID {
		return 0, NotAnUpgradeError("已经是该套餐，请直接续费")
	}
	if plan.Stackable {
		return 0, NotAnUpgradeError("可叠加的套餐不能作为升级目标")
	}
	for _, s := range subs {
		if s.PlanID == plan.ID {
			return 0, NotAnUpgradeError("已持有该套餐，请直接续费")
		}
	}
	newPeriod, err := plan.Period()
	if err != nil {
		return 0, err
	}
	var oldPrice money.Amount
	oldPeriod := newPeriod
	if fromPlan != nil {
		if fromPlan.ListPrice != nil {
			oldPrice = *fromPlan.ListPrice
		}
		if d, err := fromPlan.Period(); err == nil {
			oldPeriod = d
		}
	}
	price, err := UpgradePrice(*plan.ListPrice, newPeriod, oldPrice, oldPeriod, from.EndsAt.Sub(now))
	if err != nil {
		return 0, err
	}
	if price <= 0 {
		return 0, NotAnUpgradeError("目标套餐不比当前套餐贵，不支持降级")
	}
	return price, nil
}

// UpgradeTx replaces the (locked) subscription's snapshot with plan's in place:
// starts_at, ends_at and quota usage are kept, so rules with the same id in both
// plans keep their used amounts.
func UpgradeTx(ctx context.Context, q db.Querier, now time.Time, sub *Subscription, plan *Plan) (*Subscription, error) {
	// The raw plan JSON, exactly as GrantTx snapshots it.
	var models, rules []byte
	if err := q.QueryRow(ctx, `SELECT models, rules FROM plans WHERE id = $1`, plan.ID).Scan(&models, &rules); err != nil {
		return nil, err
	}
	if _, err := q.Exec(ctx, `UPDATE subscriptions SET plan_id = $2, plan_name = $3, models = $4, rules = $5,
			version = version + 1, updated_at = $6
		WHERE id = $1`, sub.ID, plan.ID, plan.Name, models, rules, dbTime(now)); err != nil {
		return nil, err
	}
	return getSub(ctx, q, sub.ID, false)
}
