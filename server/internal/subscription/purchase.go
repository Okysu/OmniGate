package subscription

import (
	"context"
	"fmt"
	"sort"
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
	SortCatalog(out)
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

// UpgradeQuote is the cost and effect of upgrading a subscription now.
type UpgradeQuote struct {
	// Price = Periods × the new plan's price − Credit.
	Price money.Amount
	// Credit is the value of the unused time of the old plan.
	Credit money.Amount
	// Periods is the number of new-plan periods the upgraded subscription runs
	// from now (1 unless the credit exceeds one period's price).
	Periods int
	EndsAt  time.Time
}

// QuoteUpgrade prices an upgrade the way subscription services usually do: the
// upgraded subscription starts a fresh term of the new plan now, and the unused
// time of the old plan (oldPrice per oldPeriod, remaining left) is credited.
// Upgrading early is therefore cheap and upgrading just before expiry costs
// about a new purchase — the remaining time can't be bought at a discount. A
// credit worth more than one new period (e.g. after several renewals) extends
// the new term by whole periods until it covers the credit, so nothing paid is
// lost. The credit is rounded down and the price up to PriceDecimals.
func QuoteUpgrade(newPrice money.Amount, newPeriod time.Duration, oldPrice money.Amount, oldPeriod time.Duration,
	remaining time.Duration, now time.Time) (UpgradeQuote, error) {
	if newPeriod <= 0 || oldPeriod <= 0 || newPrice <= 0 {
		return UpgradeQuote{}, fmt.Errorf("subscription: invalid upgrade quote input")
	}
	var credit money.Amount
	if remaining > 0 && oldPrice > 0 {
		c, err := oldPrice.MulDiv(int64(remaining/time.Second), int64(oldPeriod/time.Second))
		if err != nil {
			return UpgradeQuote{}, err
		}
		credit = floorAmount(c, PriceDecimals)
	}
	periods := 1
	for newPrice*money.Amount(periods) <= credit && periods < MaxPeriods {
		periods++
	}
	price := ceilAmount(newPrice*money.Amount(periods)-credit, PriceDecimals)
	return UpgradeQuote{Price: price, Credit: credit, Periods: periods, EndsAt: now.Add(newPeriod * time.Duration(periods))}, nil
}

func amountStep(decimals int) money.Amount {
	step := money.Amount(1)
	for i := decimals; i < money.Scale; i++ {
		step *= 10
	}
	return step
}

// ceilAmount rounds a non-negative amount up to decimals.
func ceilAmount(a money.Amount, decimals int) money.Amount {
	step := amountStep(decimals)
	if r := a % step; r != 0 {
		a += step - r
	}
	return a
}

// floorAmount rounds a non-negative amount down to decimals.
func floorAmount(a money.Amount, decimals int) money.Amount {
	return a - a%amountStep(decimals)
}

// UpgradeCheck validates an upgrade of from to plan for a user whose live
// subscriptions are subs and quotes it at now. Only plans that cost more per
// day than the current one are upgrades.
func UpgradeCheck(from *Subscription, fromPlan, plan *Plan, subs []*Subscription, now time.Time) (UpgradeQuote, error) {
	if from.StatusAt(now) != StatusActive {
		return UpgradeQuote{}, NotActiveError()
	}
	if from.PlanID == plan.ID {
		return UpgradeQuote{}, NotAnUpgradeError("已经是该套餐，请直接续费")
	}
	if plan.Stackable {
		return UpgradeQuote{}, NotAnUpgradeError("可叠加的套餐不能作为升级目标")
	}
	for _, s := range subs {
		if s.PlanID == plan.ID {
			return UpgradeQuote{}, NotAnUpgradeError("已持有该套餐，请直接续费")
		}
	}
	newPeriod, err := plan.Period()
	if err != nil {
		return UpgradeQuote{}, err
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
	// Daily price comparison: new/newPeriod > old/oldPeriod.
	scaled, err := plan.ListPrice.MulDiv(int64(oldPeriod/time.Second), int64(newPeriod/time.Second))
	if err != nil {
		return UpgradeQuote{}, err
	}
	if scaled <= oldPrice {
		return UpgradeQuote{}, NotAnUpgradeError("目标套餐不比当前套餐贵，不支持降级")
	}
	return QuoteUpgrade(*plan.ListPrice, newPeriod, oldPrice, oldPeriod, from.EndsAt.Sub(now), now)
}

// UpgradeTx replaces the (locked) subscription's snapshot with plan's in place
// and sets its end to endsAt (the new term, see QuoteUpgrade). starts_at and
// quota usage are kept, so rules with the same id in both plans keep their
// used amounts.
func UpgradeTx(ctx context.Context, q db.Querier, now time.Time, sub *Subscription, plan *Plan, endsAt time.Time) (*Subscription, error) {
	// The raw plan JSON, exactly as GrantTx snapshots it.
	var models, rules []byte
	if err := q.QueryRow(ctx, `SELECT models, rules FROM plans WHERE id = $1`, plan.ID).Scan(&models, &rules); err != nil {
		return nil, err
	}
	if _, err := q.Exec(ctx, `UPDATE subscriptions SET plan_id = $2, plan_name = $3, models = $4, rules = $5,
			ends_at = $6, version = version + 1, updated_at = $7
		WHERE id = $1`, sub.ID, plan.ID, plan.Name, models, rules, dbTime(endsAt), dbTime(now)); err != nil {
		return nil, err
	}
	return getSub(ctx, q, sub.ID, false)
}

// SortCatalog orders plans for customers: by price ascending, plans without a
// price last, otherwise newest first (the stable input order).
func SortCatalog(plans []*Plan) {
	sort.SliceStable(plans, func(i, j int) bool {
		a, b := plans[i].ListPrice, plans[j].ListPrice
		switch {
		case a == nil || b == nil:
			return a != nil && b == nil
		default:
			return *a < *b
		}
	})
}
