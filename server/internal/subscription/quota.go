package subscription

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/protocol"
)

// Gateway error codes (contract §3/§5).
const (
	CodeQuotaExceeded  = "quota_exceeded"
	CodeQuotaExhausted = "quota_exhausted"
)

// Decision is the pre-request quota verdict for the gateway.
type Decision struct {
	SubscriptionID *uuid.UUID // set when a subscription covers this request (skip wallet)
	Blocked        *Block     // set when the request must be rejected
	// neither set: no covering subscription, or quota is used up and the
	// effective overflow preference is "wallet" -> use the wallet
	Overflowed bool // quota used up, falling back to the wallet by preference
}

// Block describes a rejected request (HTTP 429).
type Block struct {
	Code       string        // "quota_exceeded" | "quota_exhausted"
	Message    string        // Chinese, shown to the client
	RetryAfter time.Duration // 0 when not resettable (lifetime)
}

// loadedSub is a subscription with its compiled rules.
type loadedSub struct {
	*Subscription
	rules []*compiledRule
}

type usageKey struct {
	sub  uuid.UUID
	rule string
}

// usageQuery is one (subscription, rule, since) lookup.
type usageQuery struct {
	sub   uuid.UUID
	rule  string
	since time.Time
}

// loadUsage fetches, in one indexed query, the quota_usage rows of each
// (subscription, rule) with window_start ≥ since.
func loadUsage(ctx context.Context, q db.Querier, keys []usageQuery) (map[usageKey][]usageRow, error) {
	out := map[usageKey][]usageRow{}
	if len(keys) == 0 {
		return out, nil
	}
	var rows db.Rows
	var err error
	if q.Dialect() == db.SQLite {
		// One JSON array of {s, r, t} lookups expanded with json_each; t is the
		// canonical timestamp text so >= compares times.
		type lookup struct {
			S uuid.UUID `json:"s"`
			R string    `json:"r"`
			T string    `json:"t"`
		}
		ks := make([]lookup, len(keys))
		for i, k := range keys {
			ks[i] = lookup{k.sub, k.rule, db.TimeText(k.since)}
		}
		rows, err = q.Query(ctx, `
			SELECT u.subscription_id, u.rule_id, u.window_start, u.used
			FROM json_each($1) AS k
			JOIN quota_usage u ON u.subscription_id = k.value->>'s' AND u.rule_id = k.value->>'r'
				AND u.window_start >= k.value->>'t'`, ks)
	} else {
		subs := make([]uuid.UUID, len(keys))
		rules := make([]string, len(keys))
		since := make([]time.Time, len(keys))
		for i, k := range keys {
			subs[i], rules[i], since[i] = k.sub, k.rule, k.since
		}
		rows, err = q.Query(ctx, `
			SELECT u.subscription_id, u.rule_id, u.window_start, u.used::text
			FROM unnest($1::uuid[], $2::text[], $3::timestamptz[]) AS k(sid, rid, since)
			JOIN quota_usage u ON u.subscription_id = k.sid AND u.rule_id = k.rid AND u.window_start >= k.since`,
			subs, rules, since)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k usageKey
		var r usageRow
		var used string
		if err := rows.Scan(&k.sub, &k.rule, &r.start, &used); err != nil {
			return nil, err
		}
		if r.used, err = ParseDec(used); err != nil {
			return nil, fmt.Errorf("subscription: bad usage value %q: %w", used, err)
		}
		r.start = r.start.UTC()
		out[k] = append(out[k], r)
	}
	return out, rows.Err()
}

func compileSub(s *Subscription) (*loadedSub, error) {
	rules, err := compileRules(s.Rules)
	if err != nil {
		return nil, err
	}
	return &loadedSub{Subscription: s, rules: rules}, nil
}

// Decide checks the user's live subscriptions for a request to model at now
// (contract §3). It runs two indexed queries: live subscriptions, then their
// current usage rows.
//
// keyOverflow is the API key's override ("" = follow the user's preference).
func (s *Service) Decide(ctx context.Context, userID uuid.UUID, model string, keyOverflow string, now time.Time) (*Decision, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+subCols+subFrom+`
		WHERE s.user_id = $1 AND s.status = 'active' AND s.ends_at > $2 AND s.starts_at <= $2
		ORDER BY s.ends_at, s.id`, userID, now)
	if err != nil {
		return nil, err
	}
	all, err := collectSubs(rows)
	if err != nil {
		return nil, err
	}
	var subs []*loadedSub
	var keys []usageQuery
	for _, sub := range all {
		if !covers(sub.Models, model) {
			continue
		}
		ls, err := compileSub(sub)
		if err != nil {
			s.log.ErrorContext(ctx, "skip subscription with invalid rules", "subscription", sub.ID, "err", err)
			continue
		}
		applicable := ls.rules[:0:0]
		for _, r := range ls.rules {
			if r.appliesTo(model) {
				applicable = append(applicable, r)
				keys = append(keys, usageQuery{sub.ID, r.ID, r.usageSince(sub.StartsAt, now)})
			}
		}
		ls.rules = applicable
		subs = append(subs, ls)
	}
	if len(subs) == 0 {
		return &Decision{}, nil
	}
	usage, err := loadUsage(ctx, s.pool, keys)
	if err != nil {
		return nil, err
	}
	d := decide(subs, usage, now)
	if d.Blocked == nil {
		return d, nil
	}
	pref := keyOverflow
	if pref == "" {
		if pref, err = s.QuotaOverflow(ctx, userID); err != nil {
			return nil, err
		}
	}
	if pref == OverflowWallet {
		return &Decision{Overflowed: true}, nil
	}
	d.Blocked.Message += "。可在「钱包与订阅」中设置额度用完后改用钱包余额"
	return d, nil
}

type exceededRule struct {
	rule *compiledRule
	st   ruleState
}

// decide applies contract §3 to subscriptions ordered by ends_at whose rules are
// already filtered to the model.
func decide(subs []*loadedSub, usage map[usageKey][]usageRow, now time.Time) *Decision {
	exceeded := make([][]exceededRule, len(subs))
	for i, sub := range subs {
		for _, r := range sub.rules {
			st := r.state(sub.StartsAt, now, usage[usageKey{sub.ID, r.ID}])
			if st.exceeded {
				exceeded[i] = append(exceeded[i], exceededRule{r, st})
			}
		}
		if len(exceeded[i]) == 0 {
			return &Decision{SubscriptionID: ptr(sub.ID)}
		}
	}
	// A subscription admits again once every exceeded rule has reset; one that
	// cannot reset before it ends is exhausted. Retry after the earliest.
	var best *time.Time
	var bestSub *loadedSub
	var bestRule *compiledRule
	for i, sub := range subs {
		var at *time.Time
		var why *compiledRule
		for _, e := range exceeded[i] {
			if e.st.retryAt == nil {
				at, why = nil, e.rule
				break
			}
			if at == nil || e.st.retryAt.After(*at) {
				at, why = e.st.retryAt, e.rule
			}
		}
		if at == nil || !at.Before(sub.EndsAt) {
			continue
		}
		if best == nil || at.Before(*best) {
			best, bestSub, bestRule = at, sub, why
		}
	}
	if best == nil {
		sub, e := subs[0], exceeded[0][0]
		msg := fmt.Sprintf("套餐「%s」的「%s」额度已用完", sub.PlanName, e.rule.DisplayName())
		if e.st.retryAt != nil {
			msg += "，订阅到期前不会重置"
		}
		return &Decision{Blocked: &Block{Code: CodeQuotaExhausted, Message: msg}}
	}
	retry := best.Sub(now)
	if retry < time.Second {
		retry = time.Second
	}
	retry = (retry + time.Second - 1).Truncate(time.Second)
	return &Decision{Blocked: &Block{
		Code: CodeQuotaExceeded,
		Message: fmt.Sprintf("套餐「%s」的「%s」额度已用完，将于 %s 重置", bestSub.PlanName, bestRule.DisplayName(),
			formatResetTime(*best, now, bestRule)),
		RetryAfter: retry,
	}}
}

// formatResetTime renders t in the rule's calendar timezone (UTC otherwise),
// with the date only when it is not today there.
func formatResetTime(t, now time.Time, r *compiledRule) string {
	loc := time.UTC
	if r.loc != nil {
		loc = r.loc
	}
	lt, ln := t.In(loc), now.In(loc)
	layout := "15:04 MST"
	y1, m1, d1 := lt.Date()
	y2, m2, d2 := ln.Date()
	if y1 != y2 || m1 != m2 || d1 != d2 {
		layout = "01-02 15:04 MST"
	}
	return lt.Format(layout)
}

// Record adds the usage of a finished request covered by subscriptionID to every
// applicable rule window, exactly once per requestID. Failed requests must not
// be recorded. charge is the sell-price amount (recorded even though the wallet
// is not charged).
func (s *Service) Record(ctx context.Context, subscriptionID uuid.UUID, requestID, model string, usage protocol.Usage,
	charge money.Amount, at time.Time) error {
	return s.RecordUsage(ctx, RecordInput{SubscriptionID: subscriptionID, RequestID: requestID, Model: model, Usage: usage,
		Charge: charge, At: at, Billing: BillingCtx{Model: model, ServedModel: model, ImageCount: usage.Images, AudioSeconds: float64(usage.AudioSeconds)}})
}

// RecordUsage is Record with the request context custom meters receive
// (phase9-api.md §3): their units are computed before the transaction.
func (s *Service) RecordUsage(ctx context.Context, in RecordInput) error {
	subscriptionID, requestID, model, usage, charge, at := in.SubscriptionID, in.RequestID, in.Model, in.Usage, in.Charge, in.At
	if requestID == "" {
		return fmt.Errorf("subscription: empty request id")
	}
	custom, err := s.customUnits(ctx, in)
	if err != nil {
		return err
	}
	at = dbTime(at)
	var states []QuotaState
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		states = nil
		// Locking the subscription serializes Records of one subscription, so two
		// requests cannot open two overlapping sessions.
		sub, err := getSub(ctx, tx, subscriptionID, true)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO subscription_charges (request_id, subscription_id, model, quota_charge_nano, created_at)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (request_id) DO NOTHING`,
			requestID, subscriptionID, model, int64(charge), at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // already recorded
		}
		ls, err := compileSub(sub)
		if err != nil {
			return err
		}
		type add struct {
			rule  *compiledRule
			units Dec
		}
		var adds []add
		var keys []usageQuery
		for _, r := range ls.rules {
			if !r.appliesTo(model) {
				continue
			}
			u := r.units(model, usage, charge)
			if isCustomMeter(r.Meter) {
				u = r.weighted(model, custom[r.ID])
			}
			if u.Sign() <= 0 {
				continue
			}
			adds = append(adds, add{r, u})
			if r.Window.Kind == WindowSession {
				keys = append(keys, usageQuery{sub.ID, r.ID, r.usageSince(sub.StartsAt, at)})
			}
		}
		if len(adds) == 0 {
			return nil
		}
		existing, err := loadUsage(ctx, tx, keys)
		if err != nil {
			return err
		}
		for _, a := range adds {
			ws := a.rule.recordWindow(sub.StartsAt, at, existing[usageKey{sub.ID, a.rule.ID}])
			if err := addUsage(ctx, tx, sub.ID, a.rule.ID, ws, a.units, at); err != nil {
				return err
			}
		}
		if s.OnQuota == nil {
			return nil
		}
		rules := make([]*compiledRule, len(adds))
		for i, a := range adds {
			rules[i] = a.rule
		}
		states, err = quotaStates(ctx, tx, sub, rules, at)
		return err
	})
	if err != nil {
		return err
	}
	for _, st := range states {
		s.OnQuota(ctx, st)
	}
	return nil
}

// quotaStates computes the post-Record state of rules and returns those at or
// above 80 % of their limit.
func quotaStates(ctx context.Context, q db.Querier, sub *Subscription, rules []*compiledRule, at time.Time) ([]QuotaState, error) {
	keys := make([]usageQuery, len(rules))
	for i, r := range rules {
		keys[i] = usageQuery{sub.ID, r.ID, r.usageSince(sub.StartsAt, at)}
	}
	usage, err := loadUsage(ctx, q, keys)
	if err != nil {
		return nil, err
	}
	var out []QuotaState
	for _, r := range rules {
		st := r.state(sub.StartsAt, at, usage[usageKey{sub.ID, r.ID}])
		if r.limit.Sign() <= 0 || st.used.Mul(DecInt(5)).Cmp(r.limit.Mul(DecInt(4))) < 0 {
			continue
		}
		key := ""
		switch {
		case r.Window.Kind == WindowRolling:
			key = "rolling:" + at.Truncate(r.dur).UTC().Format(time.RFC3339)
		case st.windowStart != nil:
			key = st.windowStart.UTC().Format(time.RFC3339)
		}
		out = append(out, QuotaState{UserID: sub.UserID, SubscriptionID: sub.ID, PlanName: sub.PlanName, RuleID: r.ID,
			RuleLabel: r.DisplayName(), Used: st.used.String(), Limit: r.limit.String(), Exhausted: st.exceeded,
			WindowKey: key, ResetsAt: st.resetsAt})
	}
	return out, nil
}

// addUsage adds units to one quota counter inside the settlement transaction.
// PostgreSQL adds in numeric(38,9); SQLite stores the counter as decimal text
// and the sum is computed in Go — safe because a SQLite transaction holds the
// database write lock from its start (BEGIN IMMEDIATE).
func addUsage(ctx context.Context, tx db.Tx, sub uuid.UUID, rule string, window time.Time, units Dec, at time.Time) error {
	if tx.Dialect() != db.SQLite {
		_, err := tx.Exec(ctx, `INSERT INTO quota_usage (subscription_id, rule_id, window_start, used, updated_at)
			VALUES ($1, $2, $3, $4::numeric, $5)
			ON CONFLICT (subscription_id, rule_id, window_start)
			DO UPDATE SET used = quota_usage.used + EXCLUDED.used, updated_at = EXCLUDED.updated_at`,
			sub, rule, window, units.String(), at)
		return err
	}
	var cur string
	err := tx.QueryRow(ctx, `SELECT used FROM quota_usage WHERE subscription_id = $1 AND rule_id = $2 AND window_start = $3`,
		sub, rule, window).Scan(&cur)
	total := units
	switch {
	case err == nil:
		old, err := ParseDec(cur)
		if err != nil {
			return fmt.Errorf("subscription: bad usage value %q: %w", cur, err)
		}
		total = old.Add(units)
	case !db.IsNoRows(err):
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO quota_usage (subscription_id, rule_id, window_start, used, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (subscription_id, rule_id, window_start)
		DO UPDATE SET used = EXCLUDED.used, updated_at = EXCLUDED.updated_at`,
		sub, rule, window, total.String(), at)
	return err
}

// ---- views ----

// RuleUsage is a snapshotted rule with its current window (contract §2).
type RuleUsage struct {
	Rule
	Used        string     `json:"used"`
	WindowStart *time.Time `json:"windowStart"`
	ResetsAt    *time.Time `json:"resetsAt"`
	Remaining   string     `json:"remaining"`
	Exceeded    bool       `json:"exceeded"`
}

// IDName is a reference with a display name.
type IDName struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// UserRef identifies the subscriber.
type UserRef struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"displayName"`
}

// SubscriptionView is the API form of a subscription (contract §2).
type SubscriptionView struct {
	ID        uuid.UUID   `json:"id"`
	User      UserRef     `json:"user"`
	Plan      IDName      `json:"plan"`
	Status    string      `json:"status"`
	StartsAt  time.Time   `json:"startsAt"`
	EndsAt    time.Time   `json:"endsAt"`
	Source    string      `json:"source"`
	Models    []string    `json:"models"`
	Rules     []RuleUsage `json:"rules"`
	CreatedAt time.Time   `json:"createdAt"`
}

// DescribeTx renders sub with its current usage, reading through q (which may be
// the caller's transaction).
func DescribeTx(ctx context.Context, q db.Querier, sub *Subscription, now time.Time) (*SubscriptionView, error) {
	v, err := describe(ctx, q, []*Subscription{sub}, now)
	if err != nil {
		return nil, err
	}
	return v[0], nil
}

// describe renders subscriptions with usage computed by the same engine as
// Decide, loading all usage rows in one query.
func describe(ctx context.Context, q db.Querier, subs []*Subscription, now time.Time) ([]*SubscriptionView, error) {
	loaded := make([]*loadedSub, len(subs))
	var keys []usageQuery
	for i, sub := range subs {
		ls, err := compileSub(sub)
		if err != nil {
			return nil, err
		}
		loaded[i] = ls
		for _, r := range ls.rules {
			keys = append(keys, usageQuery{sub.ID, r.ID, r.usageSince(sub.StartsAt, now)})
		}
	}
	usage, err := loadUsage(ctx, q, keys)
	if err != nil {
		return nil, err
	}
	out := make([]*SubscriptionView, len(loaded))
	for i, ls := range loaded {
		v := &SubscriptionView{
			ID: ls.ID, User: UserRef{ID: ls.UserID, DisplayName: ls.UserName}, Plan: IDName{ID: ls.PlanID, Name: ls.PlanName},
			Status: ls.StatusAt(now), StartsAt: ls.StartsAt, EndsAt: ls.EndsAt, Source: ls.Source, Models: ls.Models,
			Rules: make([]RuleUsage, len(ls.rules)), CreatedAt: ls.CreatedAt,
		}
		for j, r := range ls.rules {
			st := r.state(ls.StartsAt, now, usage[usageKey{ls.ID, r.ID}])
			v.Rules[j] = RuleUsage{Rule: r.Rule, Used: st.used.String(), WindowStart: st.windowStart,
				ResetsAt: st.resetsAt, Remaining: maxDec(r.limit.Sub(st.used), Dec{}).String(), Exceeded: st.exceeded}
		}
		out[i] = v
	}
	return out, nil
}

// Get returns one subscription view (404 when missing).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*SubscriptionView, error) {
	sub, err := getSub(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	return DescribeTx(ctx, s.pool, sub, s.now())
}
