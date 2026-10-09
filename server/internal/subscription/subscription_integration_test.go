package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
	"omnigate/internal/protocol"
)

// testDB returns an isolated, migrated database for this test (PostgreSQL or
// SQLite, see dbtest). Requires OMNIGATE_TEST_DATABASE_URL (skips otherwise).
func testDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

type fixture struct {
	t    *testing.T
	ctx  context.Context
	pool *db.DB
	svc  *Service
	now  time.Time
}

func newFixture(t *testing.T) *fixture {
	pool := testDB(t)
	log := slog.New(slog.DiscardHandler)
	f := &fixture{t: t, ctx: context.Background(), pool: pool, svc: NewService(pool, audit.NewRecorder(pool, log), log),
		now: time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)}
	f.svc.now = func() time.Time { return f.now }
	return f
}

func (f *fixture) user(role identity.Role) Actor {
	f.t.Helper()
	id := uuid.Must(uuid.NewV7())
	name := "u-" + id.String()[:8]
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO users (id, display_name, role) VALUES ($1, $2, $3)`, id, name, role); err != nil {
		f.t.Fatal(err)
	}
	return Actor{ID: id, Name: name}
}

func (f *fixture) plan(in PlanInput) *Plan {
	f.t.Helper()
	if in.Name == "" {
		in.Name = "Pro"
	}
	if in.Duration == "" {
		in.Duration = "30d"
	}
	p, err := f.svc.CreatePlan(f.ctx, f.user(identity.RoleSystemAdmin), in, RequestMeta{})
	if err != nil {
		f.t.Fatalf("create plan: %v", err)
	}
	return p
}

func (f *fixture) grant(userID, planID uuid.UUID, periods int) (*SubscriptionView, bool) {
	f.t.Helper()
	v, renewed, err := f.svc.AdminGrant(f.ctx, f.user(identity.RoleSystemAdmin), userID, planID, periods, RequestMeta{})
	if err != nil {
		f.t.Fatalf("grant: %v", err)
	}
	return v, renewed
}

func (f *fixture) decide(userID uuid.UUID, model string, now time.Time) *Decision {
	f.t.Helper()
	return f.decideKey(userID, model, "", now)
}

func (f *fixture) decideKey(userID uuid.UUID, model, keyOverflow string, now time.Time) *Decision {
	f.t.Helper()
	d, err := f.svc.Decide(f.ctx, userID, model, keyOverflow, now)
	if err != nil {
		f.t.Fatalf("decide: %v", err)
	}
	return d
}

func (f *fixture) record(subID uuid.UUID, model string, u protocol.Usage, charge string, at time.Time) {
	f.t.Helper()
	if err := f.svc.Record(f.ctx, subID, uuid.NewString(), model, u, money.MustParse(charge), at); err != nil {
		f.t.Fatalf("record: %v", err)
	}
}

func (f *fixture) mine(userID uuid.UUID) []*SubscriptionView {
	f.t.Helper()
	v, err := f.svc.ListMine(f.ctx, userID)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func errCode(err error) string {
	if err == nil {
		return ""
	}
	return apperr.As(err).Code
}

func sessionRule(limit string) Rule {
	return Rule{ID: "5h", Label: "5 小时窗口", Meter: MeterRequests, Limit: limit, Window: Window{Kind: WindowSession, Duration: "5h"}}
}

func admitted(t *testing.T, d *Decision, want uuid.UUID) {
	t.Helper()
	if d.Blocked != nil || d.SubscriptionID == nil || *d.SubscriptionID != want {
		t.Fatalf("want admission by %s, got sub=%v blocked=%+v", want, d.SubscriptionID, d.Blocked)
	}
}

func TestPlanCRUD(t *testing.T) {
	f := newFixture(t)
	admin := f.user(identity.RoleSystemAdmin)
	price := money.MustParse("19.9")
	p, err := f.svc.CreatePlan(f.ctx, admin, PlanInput{
		Name: " Pro ", Description: "for devs", ListPrice: &price, Duration: "30d", Models: []string{"gpt-5", "claude"},
		Rules: []Rule{sessionRule("45"), {ID: "weekly", Meter: MeterTokensTotal, Limit: "1000000",
			Window:       Window{Kind: WindowCalendar, Unit: "week", Timezone: "Asia/Shanghai"},
			ModelWeights: map[string]string{"claude": "2.5"}}},
	}, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Pro" || p.Status != PlanActive || p.Version != 1 || p.ListPrice == nil || *p.ListPrice != price ||
		len(p.Rules) != 2 || p.Rules[1].ModelWeights["claude"] != "2.5" {
		t.Fatalf("created plan %+v", p)
	}

	// Validation with field-level details.
	many := make([]string, 201)
	for i := range many {
		many[i] = fmt.Sprintf("m%d", i)
	}
	_, err = f.svc.CreatePlan(f.ctx, admin, PlanInput{Name: "", Duration: "10m", Models: many,
		Rules: []Rule{sessionRule("1.5"), sessionRule("2"),
			{ID: "tz", Meter: MeterRequests, Limit: "1", Window: Window{Kind: WindowCalendar, Unit: "day", Timezone: "Nowhere/City"}},
			{ID: "w", Meter: MeterCharge, Limit: "1", Window: Window{Kind: WindowLifetime}, ModelWeights: map[string]string{"x": "1000.5"}}},
	}, RequestMeta{})
	e := apperr.As(err)
	for _, k := range []string{"name", "duration", "models", "rules[0].limit", "rules[1].id", "rules[2].window.timezone",
		"rules[3].modelWeights.x"} {
		if _, ok := e.Details[k]; !ok {
			t.Errorf("missing detail %s: %v", k, e.Details)
		}
	}
	if _, err := f.svc.CreatePlan(f.ctx, admin, PlanInput{Name: "x", Duration: "1d"}, RequestMeta{}); errCode(err) != "validation_failed" {
		t.Fatalf("no rules: %v", err)
	}

	// Patch: stale version, then archive + clear price + new rules.
	name := "Pro+"
	if _, err := f.svc.UpdatePlan(f.ctx, admin, p.ID, PlanPatch{Name: &name, Version: 7}, RequestMeta{}); errCode(err) != "version_conflict" {
		t.Fatalf("stale: %v", err)
	}
	archived := PlanArchived
	rules := []Rule{sessionRule("10")}
	p2, err := f.svc.UpdatePlan(f.ctx, admin, p.ID, PlanPatch{Name: &name, Status: &archived, SetListPrice: true,
		Rules: &rules, Version: 1}, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Name != "Pro+" || p2.Status != PlanArchived || p2.ListPrice != nil || p2.Version != 2 || len(p2.Rules) != 1 ||
		p2.Description != "for devs" || len(p2.Models) != 2 {
		t.Fatalf("patched %+v", p2)
	}
	bad := "7x"
	if _, err := f.svc.UpdatePlan(f.ctx, admin, p.ID, PlanPatch{Duration: &bad, Version: 2}, RequestMeta{}); errCode(err) != "validation_failed" {
		t.Fatalf("bad patch: %v", err)
	}
	if _, err := f.svc.UpdatePlan(f.ctx, admin, uuid.New(), PlanPatch{Version: 1}, RequestMeta{}); errCode(err) != "not_found" {
		t.Fatalf("unknown plan: %v", err)
	}

	f.plan(PlanInput{Name: "Basic", Rules: []Rule{sessionRule("5")}})
	all, total, err := f.svc.ListPlans(f.ctx, "", 0, 20)
	if err != nil || total != 2 || len(all) != 2 || all[0].Name != "Basic" {
		t.Fatalf("list all: %d %v", total, err)
	}
	act, total, _ := f.svc.ListPlans(f.ctx, PlanActive, 0, 20)
	if total != 1 || act[0].Name != "Basic" {
		t.Fatalf("list active: %d", total)
	}
	var audits int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE action IN ('plan.create', 'plan.update')`).Scan(&audits)
	if audits != 3 {
		t.Fatalf("plan audits %d", audits)
	}
}

func TestGrantRenewCancel(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("10")}, Models: []string{"a"}})

	v, renewed := f.grant(u.ID, p.ID, 1)
	if renewed || v.Status != StatusActive || !v.StartsAt.Equal(f.now) || !v.EndsAt.Equal(f.now.Add(30*day)) ||
		v.Source != SourceAdmin || v.Plan.Name != "Pro" || v.User.DisplayName != u.Name || len(v.Rules) != 1 ||
		v.Rules[0].Used != "0" || v.Rules[0].Remaining != "10" || v.Rules[0].WindowStart != nil {
		t.Fatalf("grant: %+v", v)
	}

	// Plan edits do not touch the snapshot; renewal extends without re-snapshotting.
	admin := f.user(identity.RoleSystemAdmin)
	rules := []Rule{sessionRule("99")}
	newName := "Renamed"
	if _, err := f.svc.UpdatePlan(f.ctx, admin, p.ID, PlanPatch{Rules: &rules, Name: &newName, Version: 1}, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Hour)
	v2, renewed := f.grant(u.ID, p.ID, 2)
	if !renewed || v2.ID != v.ID || !v2.EndsAt.Equal(v.EndsAt.Add(60*day)) || v2.Rules[0].Limit != "10" || v2.Plan.Name != "Pro" {
		t.Fatalf("renew: renewed=%v %+v", renewed, v2)
	}

	// Stackable plans add subscriptions.
	sp := f.plan(PlanInput{Name: "Pack", Stackable: true, Duration: "7d", Rules: []Rule{{ID: "n", Meter: MeterRequests,
		Limit: "100", Window: Window{Kind: WindowLifetime}}}})
	a, r1 := f.grant(u.ID, sp.ID, 1)
	b, r2 := f.grant(u.ID, sp.ID, 1)
	if r1 || r2 || a.ID == b.ID {
		t.Fatal("stackable grants must create separate subscriptions")
	}
	if subs := f.mine(u.ID); len(subs) != 3 {
		t.Fatalf("mine: %d", len(subs))
	}

	// Errors.
	if _, _, err := f.svc.AdminGrant(f.ctx, admin, u.ID, p.ID, 0, RequestMeta{}); errCode(err) != "validation_failed" {
		t.Fatalf("periods 0: %v", err)
	}
	if _, _, err := f.svc.AdminGrant(f.ctx, admin, u.ID, p.ID, 121, RequestMeta{}); errCode(err) != "validation_failed" {
		t.Fatalf("periods 121: %v", err)
	}
	if _, _, err := f.svc.AdminGrant(f.ctx, admin, uuid.New(), p.ID, 1, RequestMeta{}); errCode(err) != "not_found" {
		t.Fatalf("unknown user: %v", err)
	}
	if _, _, err := f.svc.AdminGrant(f.ctx, admin, u.ID, uuid.New(), 1, RequestMeta{}); errCode(err) != "not_found" {
		t.Fatalf("unknown plan: %v", err)
	}
	archived := PlanArchived
	if _, err := f.svc.UpdatePlan(f.ctx, admin, sp.ID, PlanPatch{Status: &archived, Version: 1}, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.AdminGrant(f.ctx, admin, u.ID, sp.ID, 1, RequestMeta{}); errCode(err) != "plan_archived" {
		t.Fatalf("archived: %v", err)
	}
	// Archiving does not affect existing subscriptions; subscriber counts stay live.
	if pl, _ := f.svc.GetPlan(f.ctx, sp.ID); pl.Subscribers != 2 {
		t.Fatalf("subscribers %d", pl.Subscribers)
	}

	// Cancel.
	c, err := f.svc.Cancel(f.ctx, admin, v.ID, " refund ", RequestMeta{})
	if err != nil || c.Status != StatusCancelled {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	if _, err := f.svc.Cancel(f.ctx, admin, v.ID, "", RequestMeta{}); errCode(err) != "subscription_not_active" {
		t.Fatalf("cancel twice: %v", err)
	}
	if _, err := f.svc.Cancel(f.ctx, admin, uuid.New(), "", RequestMeta{}); errCode(err) != "not_found" {
		t.Fatalf("cancel unknown: %v", err)
	}
	if d := f.decide(u.ID, "a", f.now); d.SubscriptionID == nil || *d.SubscriptionID == v.ID {
		t.Fatalf("cancelled subscription still admits (or stackable ones do not): %+v", d)
	}
	// A cancelled non-stackable subscription is not renewed: a new one starts.
	v3, renewed := f.grant(u.ID, p.ID, 1)
	if renewed || v3.ID == v.ID || v3.Plan.Name != "Renamed" || v3.Rules[0].Limit != "99" {
		t.Fatalf("grant after cancel: %v %+v", renewed, v3)
	}
	// Expired subscriptions are listed as expired and filtered.
	f.now = f.now.Add(8 * day)
	list, total, err := f.svc.ListSubscriptions(f.ctx, SubscriptionFilter{UserID: &u.ID, Status: StatusExpired}, 0, 20)
	if err != nil || total != 2 || list[0].Status != StatusExpired {
		t.Fatalf("expired filter: %d %v", total, err)
	}
	_, total, _ = f.svc.ListSubscriptions(f.ctx, SubscriptionFilter{PlanID: &p.ID}, 0, 20)
	if total != 2 {
		t.Fatalf("plan filter %d", total)
	}
	_, total, _ = f.svc.ListSubscriptions(f.ctx, SubscriptionFilter{Status: StatusCancelled}, 0, 20)
	if total != 1 {
		t.Fatalf("cancelled filter %d", total)
	}
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(DISTINCT action) FROM audit_logs WHERE action IN
		('subscription.grant', 'subscription.renew', 'subscription.cancel')`).Scan(&n)
	if n != 3 {
		t.Fatalf("subscription audit actions %d", n)
	}
}

func TestSessionLimitEndToEnd(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("3")}})
	v, _ := f.grant(u.ID, p.ID, 1)

	t0 := f.now.Add(10*time.Minute + 123*time.Millisecond)
	for i := range 3 {
		at := t0.Add(time.Duration(i) * time.Minute)
		admitted(t, f.decide(u.ID, "any-model", at), v.ID)
		f.record(v.ID, "any-model", protocol.Usage{Input: 10}, "0.01", at)
	}
	now := t0.Add(3 * time.Minute)
	d := f.decide(u.ID, "any-model", now)
	sessionStart := t0.Truncate(time.Second)
	want := sessionStart.Add(5 * time.Hour).Sub(now)
	if d.Blocked == nil || d.Blocked.Code != CodeQuotaExceeded || d.Blocked.RetryAfter < want || d.Blocked.RetryAfter > want+time.Second {
		t.Fatalf("blocked: %+v (want ≈%v)", d.Blocked, want)
	}
	mine := f.mine(u.ID)[0].Rules[0]
	if mine.Used != "3" || mine.Remaining != "0" || !mine.Exceeded || !mine.WindowStart.Equal(sessionStart) ||
		!mine.ResetsAt.Equal(sessionStart.Add(5*time.Hour)) {
		t.Fatalf("usage view %+v", mine)
	}

	// After the session ends the user is admitted and the next request opens a new session.
	later := sessionStart.Add(5*time.Hour + 2*time.Second)
	admitted(t, f.decide(u.ID, "any-model", later), v.ID)
	f.record(v.ID, "any-model", protocol.Usage{}, "0", later)
	f.now = later
	mine = f.mine(u.ID)[0].Rules[0]
	if mine.Used != "1" || !mine.WindowStart.Equal(later.Truncate(time.Second)) {
		t.Fatalf("new session %+v", mine)
	}
}

func TestWeeklyTokensWithWeights(t *testing.T) {
	f := newFixture(t) // now = Thursday 2026-10-08 09:30 UTC (17:30 Shanghai)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{{ID: "weekly", Label: "每周", Meter: MeterTokensTotal, Limit: "1000",
		Window:       Window{Kind: WindowCalendar, Unit: "week", Timezone: "Asia/Shanghai"},
		ModelWeights: map[string]string{"big": "5", "mini": "0.5"}}}})
	v, _ := f.grant(u.ID, p.ID, 1)
	at := f.now.Add(time.Minute)
	f.record(v.ID, "small", protocol.Usage{Input: 60, CacheRead: 20, CacheWrite: 10, Output: 10}, "0", at) // 100
	f.record(v.ID, "big", protocol.Usage{Input: 50, Output: 50}, "0", at)                                  // 500
	f.record(v.ID, "mini", protocol.Usage{Input: 1, Output: 2}, "0", at)                                   // 1.5
	admitted(t, f.decide(u.ID, "big", at), v.ID)
	f.record(v.ID, "big", protocol.Usage{Output: 80}, "0", at) // 400 → 1001.5
	d := f.decide(u.ID, "small", at)
	monday := time.Date(2026, 10, 11, 16, 0, 0, 0, time.UTC) // Mon 00:00 Asia/Shanghai
	if d.Blocked == nil || d.Blocked.Code != CodeQuotaExceeded || d.Blocked.RetryAfter != monday.Sub(at) {
		t.Fatalf("blocked %+v want %v", d.Blocked, monday.Sub(at))
	}
	if !strings.HasPrefix(d.Blocked.Message, "套餐「Pro」的「每周」额度已用完，将于 10-12 00:00 CST 重置。") {
		t.Fatalf("message %q", d.Blocked.Message)
	}
	f.now = at
	r := f.mine(u.ID)[0].Rules[0]
	if r.Used != "1001.5" || r.Remaining != "0" || !r.Exceeded || !r.ResetsAt.Equal(monday) ||
		!r.WindowStart.Equal(monday.Add(-7*day)) {
		t.Fatalf("view %+v", r)
	}
	// Next week the counter starts over.
	admitted(t, f.decide(u.ID, "small", monday), v.ID)
}

func TestOverflowPreferenceAndModelFilters(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Models: []string{"a", "b", "c"}, Rules: []Rule{
		{ID: "daily", Meter: MeterRequests, Limit: "1", Window: Window{Kind: WindowCalendar, Unit: "day"}},
		{ID: "budget", Meter: MeterCharge, Limit: "0.05", Window: Window{Kind: WindowPeriod, Every: "7d"}, Models: []string{"c"}},
	}})
	v, _ := f.grant(u.ID, p.ID, 1)
	at := f.now.Add(time.Minute)
	f.record(v.ID, "a", protocol.Usage{Input: 1}, "0.01", at)
	// Default preference: block, with a hint about the wallet setting.
	if d := f.decide(u.ID, "a", at); d.Blocked == nil || !strings.Contains(d.Blocked.Message, "钱包") {
		t.Fatalf("default block: %+v", d.Blocked)
	}
	// The key may override the user's preference either way.
	if d := f.decideKey(u.ID, "a", OverflowWallet, at); d.Blocked != nil || d.SubscriptionID != nil || !d.Overflowed {
		t.Fatalf("key wallet override: %+v", d)
	}
	actor := Actor{ID: u.ID, Name: "u"}
	if _, err := f.svc.SetPreferences(f.ctx, actor, Preferences{QuotaOverflow: OverflowWallet}, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if d := f.decide(u.ID, "a", at); d.Blocked != nil || !d.Overflowed {
		t.Fatalf("user wallet preference: %+v", d)
	}
	if d := f.decideKey(u.ID, "a", OverflowBlock, at); d.Blocked == nil {
		t.Fatalf("key block override: %+v", d)
	}
	if _, err := f.svc.SetPreferences(f.ctx, actor, Preferences{QuotaOverflow: "maybe"}, RequestMeta{}); errCode(err) != "validation_failed" {
		t.Fatalf("invalid preference: %v", err)
	}
	if _, err := f.svc.SetPreferences(f.ctx, actor, Preferences{QuotaOverflow: OverflowBlock}, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	// Uncovered model → wallet; user without subscriptions → wallet (not an overflow).
	if d := f.decide(u.ID, "zzz", at); d.SubscriptionID != nil || d.Blocked != nil || d.Overflowed {
		t.Fatalf("uncovered: %+v", d)
	}
	if d := f.decide(f.user(identity.RoleUser).ID, "a", at); d.SubscriptionID != nil || d.Blocked != nil || d.Overflowed {
		t.Fatalf("no subs: %+v", d)
	}
	// The charge rule only meters model c: two c requests at 0.03 exceed it.
	f.now = f.now.Add(day)
	at = f.now
	f.record(v.ID, "c", protocol.Usage{}, "0.03", at)
	f.now = f.now.Add(day)
	at = f.now
	admitted(t, f.decide(u.ID, "c", at), v.ID)
	f.record(v.ID, "c", protocol.Usage{}, "0.03", at)
	d := f.decide(u.ID, "c", at)
	// daily and budget both exceeded → blocked until the later reset (period end at grant + 7d).
	periodEnd := v.StartsAt.Add(7 * day)
	if d.Blocked == nil || d.Blocked.Code != CodeQuotaExceeded || d.Blocked.RetryAfter != periodEnd.Sub(at) {
		t.Fatalf("budget: %+v want %v", d.Blocked, periodEnd.Sub(at))
	}
	// Model b is not metered by the budget rule: it only waits for the daily reset.
	if d := f.decide(u.ID, "b", at); d.Blocked == nil || d.Blocked.RetryAfter >= periodEnd.Sub(at) {
		t.Fatalf("b: %+v", d.Blocked)
	}
	// Recorded quota charge is kept per request.
	var sum int64
	_ = f.pool.QueryRow(f.ctx, `SELECT sum(quota_charge_nano) FROM subscription_charges WHERE subscription_id = $1`, v.ID).Scan(&sum)
	if money.Amount(sum) != money.MustParse("0.07") {
		t.Fatalf("quota charges %s", money.Amount(sum))
	}
}

func TestLifetimeAndEarliestEnding(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	trial := f.plan(PlanInput{Name: "Trial", Stackable: true, Duration: "10d", Rules: []Rule{
		{ID: "total", Label: "试用次数", Meter: MeterRequests, Limit: "2", Window: Window{Kind: WindowLifetime}}}})
	long, _ := f.grant(u.ID, trial.ID, 3)  // ends +30d
	short, _ := f.grant(u.ID, trial.ID, 1) // ends +10d → preferred
	at := f.now.Add(time.Minute)
	for range 2 {
		admitted(t, f.decide(u.ID, "m", at), short.ID)
		f.record(short.ID, "m", protocol.Usage{}, "0", at)
	}
	admitted(t, f.decide(u.ID, "m", at), long.ID)
	f.record(long.ID, "m", protocol.Usage{}, "0", at)
	f.record(long.ID, "m", protocol.Usage{}, "0", at)
	d := f.decide(u.ID, "m", at)
	if d.Blocked == nil || d.Blocked.Code != CodeQuotaExhausted || d.Blocked.RetryAfter != 0 ||
		!strings.HasPrefix(d.Blocked.Message, "套餐「Trial」的「试用次数」额度已用完。") {
		t.Fatalf("exhausted: %+v", d.Blocked)
	}
}

func TestRecordIdempotentAndConcurrent(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("1000"),
		{ID: "roll", Meter: MeterTokensOutput, Limit: "1000", Window: Window{Kind: WindowRolling, Duration: "1h"}}}})
	v, _ := f.grant(u.ID, p.ID, 1)
	at := f.now.Add(time.Minute)
	// Concurrent first requests open exactly one session.
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.svc.Record(f.ctx, v.ID, fmt.Sprintf("c-%d", i), "m", protocol.Usage{Output: 1}, 0,
				at.Add(time.Duration(i)*time.Second)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for range 3 {
		if err := f.svc.Record(f.ctx, v.ID, "req-1", "m", protocol.Usage{Output: 7}, 0, at.Add(20*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	f.now = at.Add(time.Minute)
	rules := f.mine(u.ID)[0].Rules
	if rules[0].Used != "11" || rules[1].Used != "17" {
		t.Fatalf("used %s / %s", rules[0].Used, rules[1].Used)
	}
	var sessions int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM quota_usage WHERE rule_id = '5h'`).Scan(&sessions)
	if sessions != 1 {
		t.Fatalf("session rows %d", sessions)
	}
	if err := f.svc.Record(f.ctx, uuid.New(), "x", "m", protocol.Usage{}, 0, at); errCode(err) != "not_found" {
		t.Fatalf("unknown subscription: %v", err)
	}
}

func TestMigrationDown(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("1")}})
	f.grant(u.ID, p.ID, 1)
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO redeem_batches (id, kind, plan_id, periods, count, created_by)
		VALUES ($1, 'plan', $2, 1, 1, $3)`, uuid.New(), p.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	// The payload check rejects inconsistent batches.
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO redeem_batches (id, kind, count, created_by)
		VALUES ($1, 'wallet_credit', 1, $2)`, uuid.New(), u.ID); err == nil {
		t.Fatal("wallet batch without amount accepted")
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO redeem_batches (id, kind, plan_id, periods, count, created_by)
		VALUES ($1, 'plan', $2, 121, 1, $3)`, uuid.New(), p.ID, u.ID); err == nil {
		t.Fatal("periods 121 accepted")
	}
	if err := db.MigrateDownTo(f.ctx, f.pool, 5); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := db.Migrate(f.ctx, f.pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// ---- HTTP ----

type httpHarness struct {
	t   *testing.T
	srv *httptest.Server
}

func newHTTP(t *testing.T, f *fixture) *httpHarness {
	h := NewHandler(f.svc)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			var p *authz.Principal
			if id, err := uuid.Parse(req.Header.Get("X-Test-User")); err == nil {
				p = &authz.Principal{UserID: id, Name: "t", Role: identity.Role(req.Header.Get("X-Test-Role"))}
			}
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	r.Route("/api", func(r chi.Router) {
		h.Routes(r)
		r.Route("/admin", h.AdminRoutes)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &httpHarness{t: t, srv: srv}
}

func (h *httpHarness) do(as Actor, role identity.Role, method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", as.ID.String())
	req.Header.Set("X-Test-Role", string(role))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func errOf(m map[string]any) (string, map[string]any) {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	d, _ := e["details"].(map[string]any)
	return c, d
}

func TestHTTP(t *testing.T) {
	f := newFixture(t)
	h := newHTTP(t, f)
	admin := f.user(identity.RoleSystemAdmin)
	user := f.user(identity.RoleUser)
	const A, U = identity.RoleSystemAdmin, identity.RoleUser

	planBody := map[string]any{
		"name": "Pro", "description": "d", "listPrice": "20", "duration": "30d", "models": []string{"gpt"},
		"rules": []map[string]any{
			{"id": "5h", "label": "5 小时窗口", "meter": "requests", "window": map[string]any{"kind": "session", "duration": "5h"},
				"limit": "2"},
			{"id": "weekly", "meter": "tokens.total", "window": map[string]any{"kind": "calendar", "unit": "week"},
				"limit": "1000", "modelWeights": map[string]string{"gpt": "2"}},
		},
	}
	if st, _ := h.do(user, U, "POST", "/api/admin/billing/plans", planBody); st != 403 {
		t.Fatalf("user creating plan: %d", st)
	}
	st, body := h.do(admin, A, "POST", "/api/admin/billing/plans", planBody)
	if st != 201 || body["listPrice"] != "20" || body["status"] != "active" || body["version"] != float64(1) ||
		body["subscribers"] != float64(0) {
		t.Fatalf("create: %d %v", st, body)
	}
	rules := body["rules"].([]any)
	weekly := rules[1].(map[string]any)
	if _, ok := weekly["onExceed"]; ok || weekly["window"].(map[string]any)["timezone"] != "UTC" || weekly["label"] != "" {
		t.Fatalf("defaults: %v", weekly)
	}
	planID := body["id"].(string)

	// Field-level validation.
	st, body = h.do(admin, A, "POST", "/api/admin/billing/plans", map[string]any{"name": "x", "duration": "1d",
		"listPrice": "abc", "rules": []map[string]any{{"id": "x", "meter": "requests", "window": map[string]any{"kind": "rolling", "duration": "2m"}, "limit": "1"}}})
	code, details := errOf(body)
	if st != 422 || code != "validation_failed" || details["listPrice"] == nil {
		t.Fatalf("invalid price: %d %v", st, body)
	}
	st, body = h.do(admin, A, "POST", "/api/admin/billing/plans", map[string]any{"name": "x", "duration": "1d",
		"rules": []map[string]any{{"id": "x", "meter": "requests", "window": map[string]any{"kind": "rolling", "duration": "2m"}, "limit": "1"}}})
	if _, details = errOf(body); st != 422 || details["rules[0].window.duration"] == nil {
		t.Fatalf("invalid duration: %d %v", st, body)
	}
	if st, _ := h.do(admin, A, "POST", "/api/admin/billing/plans", map[string]any{"name": "x", "bogus": 1}); st != 422 {
		t.Fatalf("unknown field: %d", st)
	}

	// Patch.
	if st, body := h.do(admin, A, "PATCH", "/api/admin/billing/plans/"+planID, map[string]any{"name": "Pro 2"}); st != 422 {
		t.Fatalf("patch without version: %d %v", st, body)
	}
	st, body = h.do(admin, A, "PATCH", "/api/admin/billing/plans/"+planID, map[string]any{"description": "new", "listPrice": nil, "version": 1})
	if st != 200 || body["listPrice"] != nil || body["description"] != "new" || body["name"] != "Pro" || body["version"] != float64(2) {
		t.Fatalf("patch: %d %v", st, body)
	}
	if st, body := h.do(admin, A, "PATCH", "/api/admin/billing/plans/"+planID, map[string]any{"name": "x", "version": 1}); st != 409 {
		t.Fatalf("patch stale: %d %v", st, body)
	}

	// Catalog.
	st, body = h.do(user, U, "GET", "/api/plans", nil)
	if st != 200 || body["total"] != float64(1) {
		t.Fatalf("catalog: %d %v", st, body)
	}
	for _, admin := range []string{"subscribers", "status", "version", "createdAt"} {
		if _, ok := body["items"].([]any)[0].(map[string]any)[admin]; ok {
			t.Fatalf("catalog leaks admin field %q: %v", admin, body)
		}
	}
	if st, body := h.do(admin, A, "GET", "/api/admin/billing/plans?status=archived", nil); st != 200 || body["total"] != float64(0) {
		t.Fatalf("admin list: %d %v", st, body)
	}

	// Grant, renew.
	grant := map[string]any{"userId": user.ID, "planId": planID, "periods": 1}
	st, body = h.do(admin, A, "POST", "/api/admin/billing/subscriptions", grant)
	if st != 201 || body["status"] != "active" || body["source"] != "admin" || body["user"].(map[string]any)["displayName"] != user.Name {
		t.Fatalf("grant: %d %v", st, body)
	}
	subID := body["id"].(string)
	r0 := body["rules"].([]any)[0].(map[string]any)
	if r0["used"] != "0" || r0["windowStart"] != nil || r0["resetsAt"] != nil || r0["remaining"] != "2" || r0["exceeded"] != false {
		t.Fatalf("rule usage: %v", r0)
	}
	if st, body := h.do(admin, A, "POST", "/api/admin/billing/subscriptions", grant); st != 200 || body["id"] != subID {
		t.Fatalf("renew: %d %v", st, body)
	}
	if st, body := h.do(admin, A, "POST", "/api/admin/billing/subscriptions", map[string]any{"userId": user.ID}); st != 422 {
		t.Fatalf("grant missing fields: %d %v", st, body)
	}

	// Usage shows up in the user's view.
	sid := uuid.MustParse(subID)
	f.record(sid, "gpt", protocol.Usage{Input: 10, Output: 5}, "0", f.now)
	st, body = h.do(user, U, "GET", "/api/billing/subscriptions", nil)
	items := body["items"].([]any)
	if st != 200 || len(items) != 1 {
		t.Fatalf("mine: %d %v", st, body)
	}
	rs := items[0].(map[string]any)["rules"].([]any)
	if rs[0].(map[string]any)["used"] != "1" || rs[1].(map[string]any)["used"] != "30" || rs[1].(map[string]any)["remaining"] != "970" {
		t.Fatalf("mine usage: %v", rs)
	}
	if st, _ := h.do(user, identity.RoleAuditor, "GET", "/api/billing/subscriptions", nil); st != 403 {
		t.Fatalf("auditor: %d", st)
	}

	// Admin list with filters.
	st, body = h.do(admin, A, "GET", "/api/admin/billing/subscriptions?userId="+user.ID.String()+"&status=active", nil)
	if st != 200 || body["total"] != float64(1) {
		t.Fatalf("admin subs: %d %v", st, body)
	}
	if st, _ := h.do(admin, A, "GET", "/api/admin/billing/subscriptions?status=bogus", nil); st != 422 {
		t.Fatalf("bad status: %d", st)
	}
	if st, _ := h.do(admin, A, "GET", "/api/admin/billing/subscriptions?userId=nope", nil); st != 422 {
		t.Fatalf("bad userId: %d", st)
	}

	// Cancel.
	st, body = h.do(admin, A, "POST", "/api/admin/billing/subscriptions/"+subID+"/cancel", map[string]any{"note": "abuse"})
	if st != 200 || body["status"] != "cancelled" {
		t.Fatalf("cancel: %d %v", st, body)
	}
	st, body = h.do(admin, A, "POST", "/api/admin/billing/subscriptions/"+subID+"/cancel", nil)
	if code, _ := errOf(body); st != 409 || code != "subscription_not_active" {
		t.Fatalf("cancel twice: %d %v", st, body)
	}
	if st, _ := h.do(admin, A, "POST", "/api/admin/billing/subscriptions/"+uuid.NewString()+"/cancel", nil); st != 404 {
		t.Fatalf("cancel unknown: %d", st)
	}

	// Archived plans cannot be granted.
	st, body = h.do(admin, A, "PATCH", "/api/admin/billing/plans/"+planID, map[string]any{"status": "archived", "version": 2})
	if st != 200 {
		t.Fatalf("archive: %d %v", st, body)
	}
	st, body = h.do(admin, A, "POST", "/api/admin/billing/subscriptions", grant)
	if code, _ := errOf(body); st != 409 || code != "plan_archived" {
		t.Fatalf("grant archived: %d %v", st, body)
	}
	if st, body := h.do(user, U, "GET", "/api/plans", nil); st != 200 || body["total"] != float64(0) {
		t.Fatalf("catalog after archive: %d %v", st, body)
	}
}

func TestSweepKeepsLiveCounters(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Duration: "366d", Rules: []Rule{sessionRule("100")}})
	v, _ := f.grant(u.ID, p.ID, 1)
	t0 := f.now.Add(time.Minute)
	f.record(v.ID, "m", protocol.Usage{Input: 1}, "0", t0)
	count := func() (n int) {
		_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM quota_usage WHERE subscription_id = $1`, v.ID).Scan(&n)
		return
	}
	if _, err := f.svc.Sweep(f.ctx, t0.Add(time.Hour)); err != nil || count() != 1 {
		t.Fatalf("live session row swept: count=%d err=%v", count(), err)
	}
	if _, err := f.svc.Sweep(f.ctx, t0.Add(40*24*time.Hour)); err != nil || count() != 0 {
		t.Fatalf("stale session row kept: count=%d err=%v", count(), err)
	}
}

// TestRecordConcurrentExactSums records many requests concurrently against
// one subscription with fractional charges. On SQLite the counters are decimal
// text added in Go inside the (immediate) transaction; on PostgreSQL they are
// numeric(38,9). Both must give the exact sum with no lost updates.
func TestRecordConcurrentExactSums(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{
		{ID: "cost", Meter: MeterCharge, Limit: "1000000", Window: Window{Kind: WindowCalendar, Unit: "month"}},
		{ID: "req", Meter: MeterRequests, Limit: "100000", Window: Window{Kind: WindowLifetime}},
	}})
	v, _ := f.grant(u.ID, p.ID, 1)
	at := f.now.Add(time.Minute)
	const workers, each = 8, 15
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				// 0.000000001 + 0.1 per request: exercises the 9th decimal.
				if err := f.svc.Record(f.ctx, v.ID, fmt.Sprintf("r-%d-%d", w, i), "m", protocol.Usage{Output: 1},
					money.MustParse("0.100000001"), at.Add(time.Duration(i)*time.Millisecond)); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	rules := f.mine(u.ID)[0].Rules
	if rules[0].Used != "12.00000012" || rules[1].Used != "120" {
		t.Fatalf("used %s / %s", rules[0].Used, rules[1].Used)
	}
}
