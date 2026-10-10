package subscription

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/identity"
	"omnigate/internal/protocol"
)

// phase11-api.md §1 (anchored reset) and §2 (reset cards).

func rollingRule(id, dur, limit string) Rule {
	return Rule{ID: id, Meter: MeterRequests, Limit: limit, Window: Window{Kind: WindowRolling, Duration: dur}}
}

func weeklySession(limit string) Rule {
	return Rule{ID: "weekly", Label: "每周", Meter: MeterRequests, Limit: limit, Window: Window{Kind: WindowSession, Duration: "7d"}}
}

func monthlyRule(limit string) Rule {
	return Rule{ID: "monthly", Meter: MeterRequests, Limit: limit, Window: Window{Kind: WindowPeriod, Every: "30d"}}
}

// rulesOf returns rule id → usage of the user's subscription subID at f.now.
func (f *fixture) rulesOf(userID, subID uuid.UUID) map[string]RuleUsage {
	f.t.Helper()
	for _, v := range f.mine(userID) {
		if v.ID == subID {
			out := map[string]RuleUsage{}
			for _, r := range v.Rules {
				out[r.ID] = r
			}
			return out
		}
	}
	f.t.Fatalf("subscription %s not found", subID)
	return nil
}

func (f *fixture) reset(subID uuid.UUID, rules []string) {
	f.t.Helper()
	if _, err := f.svc.ResetQuota(f.ctx, f.user(identity.RoleSystemAdmin), ResetInput{Target: Target{IDs: []uuid.UUID{subID}}, Rules: rules},
		false, RequestMeta{}); err != nil {
		f.t.Fatalf("reset: %v", err)
	}
}

func TestAnchoredSessionReset(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("3"), rollingRule("roll", "5h", "3"), monthlyRule("100")}})
	v, _ := f.grant(u.ID, p.ID, 1)

	t0 := f.now.Add(10 * time.Minute)
	for i := range 3 {
		f.record(v.ID, "m", protocol.Usage{}, "0", t0.Add(time.Duration(i)*time.Minute))
	}
	f.now = t0.Add(2 * time.Hour)
	if d := f.decide(u.ID, "m", f.now); d.Blocked == nil {
		t.Fatal("expected the exhausted session to block")
	}

	// Reset at A (with a sub-second part): the session restarts at A.
	a := f.now.Add(456 * time.Millisecond)
	f.now = a
	f.reset(v.ID, nil)
	anchor := a.Truncate(time.Second)
	rules := f.rulesOf(u.ID, v.ID)
	s, roll, month := rules["5h"], rules["roll"], rules["monthly"]
	if s.Used != "0" || s.WindowStart == nil || !s.WindowStart.Equal(anchor) || !s.ResetsAt.Equal(anchor.Add(5*time.Hour)) || s.Exceeded {
		t.Fatalf("session after reset: %+v", s)
	}
	if roll.Used != "0" || roll.ResetsAt != nil || month.Used != "0" {
		t.Fatalf("rolling / period after reset: %+v / %+v", roll, month)
	}
	admitted(t, f.decide(u.ID, "m", a), v.ID)
	var buckets int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM quota_usage WHERE subscription_id = $1 AND rule_id = 'roll'`, v.ID).Scan(&buckets)
	if buckets != 0 {
		t.Fatalf("rolling buckets left: %d", buckets)
	}
	// The empty anchor row survives the sweeper.
	if _, err := f.svc.Sweep(f.ctx, a); err != nil {
		t.Fatal(err)
	}

	// Usage after the reset accumulates into the anchored window.
	f.record(v.ID, "m", protocol.Usage{}, "0", a.Add(time.Hour))
	f.record(v.ID, "m", protocol.Usage{}, "0", a.Add(2*time.Hour))
	f.now = a.Add(2 * time.Hour)
	s = f.rulesOf(u.ID, v.ID)["5h"]
	if s.Used != "2" || !s.WindowStart.Equal(anchor) || !s.ResetsAt.Equal(anchor.Add(5*time.Hour)) {
		t.Fatalf("session usage after reset: %+v", s)
	}
	var sessions int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM quota_usage WHERE subscription_id = $1 AND rule_id = '5h'`, v.ID).Scan(&sessions)
	if sessions != 1 {
		t.Fatalf("session rows: %d", sessions)
	}

	// At A + 5h the anchored session is over; the next request opens a new one.
	f.now = anchor.Add(5 * time.Hour)
	if s = f.rulesOf(u.ID, v.ID)["5h"]; s.WindowStart != nil || s.Used != "0" {
		t.Fatalf("session after refresh: %+v", s)
	}
	next := anchor.Add(5*time.Hour + 90*time.Second)
	f.record(v.ID, "m", protocol.Usage{}, "0", next)
	f.now = next
	if s = f.rulesOf(u.ID, v.ID)["5h"]; s.Used != "1" || !s.WindowStart.Equal(next.Truncate(time.Second)) {
		t.Fatalf("new session: %+v", s)
	}

	// Resetting an idle session (no live session) also anchors it.
	f.now = next.Add(10 * time.Hour)
	f.reset(v.ID, []string{"5h"})
	if s = f.rulesOf(u.ID, v.ID)["5h"]; s.Used != "0" || !s.WindowStart.Equal(f.now.Truncate(time.Second)) {
		t.Fatalf("idle reset: %+v", s)
	}
	// Two resets in the same second keep one anchor row.
	f.reset(v.ID, []string{"5h"})
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM quota_usage WHERE subscription_id = $1 AND rule_id = '5h' AND window_start >= $2`,
		v.ID, f.now.Add(-5*time.Hour)).Scan(&sessions)
	if sessions != 1 {
		t.Fatalf("anchor rows: %d", sessions)
	}
}

// ---- reset cards ----

func (f *fixture) issue(in IssueCardsInput) *IssueCardsResult {
	f.t.Helper()
	if in.Quantity == 0 {
		in.Quantity = 1
	}
	res, err := f.svc.IssueCards(f.ctx, f.user(identity.RoleSystemAdmin), in, false, RequestMeta{})
	if err != nil {
		f.t.Fatalf("issue: %v", err)
	}
	return res
}

// myCards returns the user's cards of a batch.
func (f *fixture) myCards(userID, batchID uuid.UUID) []*Card {
	f.t.Helper()
	mine, err := f.svc.ListMyCards(f.ctx, userID)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []*Card
	for _, c := range mine.Items {
		if c.BatchID == batchID {
			out = append(out, c)
		}
	}
	return out
}

func usersTarget(ids ...uuid.UUID) CardTarget { return CardTarget{Type: CardTargetUsers, UserIDs: ids} }

func TestCardIssueTargets(t *testing.T) {
	f := newFixture(t)
	admin := f.user(identity.RoleSystemAdmin)
	a, b, c := f.user(identity.RoleUser), f.user(identity.RoleUser), f.user(identity.RoleChannelAdmin)
	disabled, auditor := f.user(identity.RoleUser), f.user(identity.RoleAuditor)
	if _, err := f.pool.Exec(f.ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, disabled.ID); err != nil {
		t.Fatal(err)
	}
	group := uuid.Must(uuid.NewV7())
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO user_groups (id, name) VALUES ($1, '测试-组')`, group); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE users SET group_id = $1 WHERE id = ANY($2)`, group, []uuid.UUID{a.ID, disabled.ID}); err != nil {
		t.Fatal(err)
	}
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("10")}})
	f.grant(b.ID, p.ID, 1)
	f.grant(c.ID, p.ID, 1)
	cancelled, _ := f.grant(a.ID, p.ID, 1)
	if _, err := f.svc.Cancel(f.ctx, admin, cancelled.ID, "", RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	var everyone int // active users allowed billing.own (fixture admins included)
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM users WHERE status = 'active' AND role <> 'auditor'`).Scan(&everyone)

	dry := func(tg CardTarget, qty int) *IssueCardsResult {
		t.Helper()
		res, err := f.svc.IssueCards(f.ctx, admin, IssueCardsInput{Kind: CardKind5h, Quantity: qty, Target: tg}, true, RequestMeta{})
		if err != nil {
			t.Fatalf("dry run %+v: %v", tg, err)
		}
		if res.Batch != nil || res.Cards != res.Recipients*qty {
			t.Fatalf("dry run result %+v", res)
		}
		return res
	}
	for i, tc := range []struct {
		target CardTarget
		want   int
	}{
		{usersTarget(a.ID, b.ID, disabled.ID, auditor.ID, a.ID, uuid.New()), 2},
		{CardTarget{Type: CardTargetGroup, GroupID: &group}, 1},
		{CardTarget{Type: CardTargetPlan, PlanID: &p.ID}, 2},
		{CardTarget{Type: CardTargetAll}, everyone},
	} {
		if res := dry(tc.target, 3); res.Recipients != tc.want {
			t.Fatalf("case %d: %d recipients, want %d", i, res.Recipients, tc.want)
		}
	}
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM reset_cards`).Scan(&n)
	if n != 0 {
		t.Fatalf("dry run wrote %d cards", n)
	}

	// Validation and unknown references.
	for _, in := range []IssueCardsInput{
		{Kind: "daily", Quantity: 1, Target: CardTarget{Type: CardTargetAll}},
		{Kind: CardKind5h, Quantity: 101, Target: CardTarget{Type: CardTargetAll}},
		{Kind: CardKind5h, Quantity: 1, Target: CardTarget{Type: "nobody"}},
		{Kind: CardKind5h, Quantity: 1, Target: usersTarget()},
		{Kind: CardKind5h, Quantity: 1, Target: CardTarget{Type: CardTargetGroup}},
		{Kind: CardKind5h, Quantity: 1, Target: CardTarget{Type: CardTargetAll}, ExpiresAt: ptr(f.now.Add(-time.Minute))},
		{Kind: CardKind5h, Quantity: 1, Target: CardTarget{Type: CardTargetAll}, PlanIDs: []uuid.UUID{uuid.New()}},
		{Kind: CardKind5h, Quantity: 1, Target: usersTarget(disabled.ID)}, // nobody eligible
	} {
		if _, err := f.svc.IssueCards(f.ctx, admin, in, false, RequestMeta{}); errCode(err) != "validation_failed" {
			t.Fatalf("issue %+v: %v", in, err)
		}
	}
	missing := uuid.New()
	if _, err := f.svc.IssueCards(f.ctx, admin, IssueCardsInput{Kind: CardKind5h, Quantity: 1,
		Target: CardTarget{Type: CardTargetPlan, PlanID: &missing}}, true, RequestMeta{}); errCode(err) != "not_found" {
		t.Fatalf("unknown plan: %v", err)
	}
	if _, err := f.svc.IssueCards(f.ctx, admin, IssueCardsInput{Kind: CardKind5h, Quantity: 1,
		Target: CardTarget{Type: CardTargetGroup, GroupID: &missing}}, true, RequestMeta{}); errCode(err) != "not_found" {
		t.Fatalf("unknown group: %v", err)
	}

	// Real issues materialize quantity × recipients cards (issued by admin:
	// f.issue would add an administrator, and a recipient, per call).
	issue := func(in IssueCardsInput) *IssueCardsResult {
		t.Helper()
		res, err := f.svc.IssueCards(f.ctx, admin, in, false, RequestMeta{})
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		return res
	}
	exp := f.now.Add(48 * time.Hour)
	byGroup := issue(IssueCardsInput{Kind: CardKindWeekly, Quantity: 2, Target: CardTarget{Type: CardTargetGroup, GroupID: &group},
		ExpiresAt: &exp, PlanIDs: []uuid.UUID{p.ID}, Note: "补偿"})
	bt := byGroup.Batch
	if byGroup.Recipients != 1 || byGroup.Cards != 2 || bt.Counts != (CardCounts{Issued: 2, Available: 2}) || bt.Target.GroupName != "测试-组" ||
		len(bt.Plans) != 1 || bt.Plans[0].Name != "Pro" || !bt.ExpiresAt.Equal(exp) || bt.Status != CardBatchActive || bt.Note != "补偿" {
		t.Fatalf("group batch %+v", bt)
	}
	if cards := f.myCards(a.ID, bt.ID); len(cards) != 2 || cards[0].Kind != CardKindWeekly || cards[0].Status != CardAvailable ||
		cards[0].Note != "补偿" || cards[0].Plans[0].ID != p.ID {
		t.Fatalf("a's cards %+v", cards)
	}
	byPlan := issue(IssueCardsInput{Kind: CardKind5h, Quantity: 1, Target: CardTarget{Type: CardTargetPlan, PlanID: &p.ID}})
	if byPlan.Recipients != 2 || byPlan.Batch.Target.PlanName != "Pro" || len(f.myCards(b.ID, byPlan.Batch.ID)) != 1 ||
		len(f.myCards(a.ID, byPlan.Batch.ID)) != 0 {
		t.Fatalf("plan batch %+v", byPlan)
	}
	all := issue(IssueCardsInput{Kind: CardKindBoth, Quantity: 3, Target: CardTarget{Type: CardTargetAll}})
	if all.Recipients != everyone || all.Cards != 3*everyone || len(f.myCards(disabled.ID, all.Batch.ID)) != 0 ||
		len(f.myCards(auditor.ID, all.Batch.ID)) != 0 {
		t.Fatalf("all batch %+v", all)
	}
	mine, _ := f.svc.ListMyCards(f.ctx, a.ID)
	if mine.Available[CardKindWeekly] != 2 || mine.Available[CardKindBoth] != 3 || mine.Available[CardKind5h] != 0 {
		t.Fatalf("available %+v", mine.Available)
	}

	// Revoke: unused cards become revoked, used ones stay used.
	sub := f.mine(b.ID)[0]
	used := f.myCards(b.ID, all.Batch.ID)[0]
	if _, err := f.svc.UseCard(f.ctx, b, used.ID, sub.ID, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	rb, err := f.svc.RevokeCardBatch(f.ctx, admin, all.Batch.ID, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if rb.Status != CardBatchRevoked || rb.RevokedAt == nil || rb.Counts != (CardCounts{Issued: 3 * everyone, Used: 1, Revoked: 3*everyone - 1}) {
		t.Fatalf("revoked batch %+v", rb)
	}
	if _, err := f.svc.RevokeCardBatch(f.ctx, admin, all.Batch.ID, RequestMeta{}); errCode(err) != "card_batch_revoked" {
		t.Fatalf("revoke twice: %v", err)
	}
	if _, err := f.svc.RevokeCardBatch(f.ctx, admin, uuid.New(), RequestMeta{}); errCode(err) != "not_found" {
		t.Fatalf("revoke unknown: %v", err)
	}
	batches, total, err := f.svc.ListCardBatches(f.ctx, 0, 10)
	if err != nil || total != 3 || batches[0].ID != all.Batch.ID || batches[2].ID != bt.ID {
		t.Fatalf("batches %d %v", total, err)
	}
	cards, total, err := f.svc.ListCards(f.ctx, CardFilter{UserID: &a.ID}, 0, 50)
	if err != nil || total != 5 || len(cards) != 5 || cards[0].User.ID != a.ID {
		t.Fatalf("a's cards: %d %v", total, err)
	}
	if _, total, _ = f.svc.ListCards(f.ctx, CardFilter{BatchID: &byPlan.Batch.ID}, 0, 50); total != 2 {
		t.Fatalf("batch cards: %d", total)
	}

	// Audit: one entry per issue and revoke.
	var issued, revoked int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE action = 'reset_card.issue'`).Scan(&issued)
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE action = 'reset_card.revoke'`).Scan(&revoked)
	if issued != 3 || revoked != 1 {
		t.Fatalf("audits issue=%d revoke=%d", issued, revoked)
	}
}

func TestCardIssueNotifies(t *testing.T) {
	f := newFixture(t)
	var got []CardNotice
	f.svc.OnCards = func(_ context.Context, n CardNotice) { got = append(got, n) }
	users := make([]uuid.UUID, 0, cardNoticeChunk+5)
	for range cardNoticeChunk + 5 {
		users = append(users, f.user(identity.RoleUser).ID)
	}
	res := f.issue(IssueCardsInput{Kind: CardKind5h, Quantity: 2, Target: usersTarget(users...), Note: "n"})
	if len(got) != 1 || got[0].BatchID != res.Batch.ID || got[0].Quantity != 2 || got[0].Note != "n" || len(got[0].Users) != 2 ||
		len(got[0].Users[0]) != cardNoticeChunk || len(got[0].Users[1]) != 5 {
		t.Fatalf("notices %+v", got)
	}
	if _, err := f.svc.IssueCards(f.ctx, f.user(identity.RoleSystemAdmin), IssueCardsInput{Kind: CardKind5h, Quantity: 1,
		Target: usersTarget(users[0])}, true, RequestMeta{}); err != nil || len(got) != 1 {
		t.Fatalf("dry run notified: %v %d", err, len(got))
	}
}

func TestCardUse(t *testing.T) {
	f := newFixture(t)
	u, other := f.user(identity.RoleUser), f.user(identity.RoleUser)
	pro := f.plan(PlanInput{Name: "Pro", Stackable: true, Rules: []Rule{sessionRule("10"), weeklySession("50"), monthlyRule("100")}})
	daily := f.plan(PlanInput{Name: "Daily", Stackable: true, Rules: []Rule{{ID: "d", Meter: MeterRequests, Limit: "5",
		Window: Window{Kind: WindowCalendar, Unit: "day"}}, rollingRule("6h", "6h", "5")}})
	rolling := f.plan(PlanInput{Name: "Roll", Stackable: true, Rules: []Rule{rollingRule("r5", "5h", "5"), rollingRule("r7", "168h", "9")}})
	sub, _ := f.grant(u.ID, pro.ID, 1)
	dsub, _ := f.grant(u.ID, daily.ID, 1)
	rsub, _ := f.grant(u.ID, rolling.ID, 1)
	osub, _ := f.grant(other.ID, pro.ID, 1)

	use := func(c *Card, subID uuid.UUID) (*UseCardResult, error) {
		return f.svc.UseCard(f.ctx, u, c.ID, subID, RequestMeta{})
	}
	card := func(kind string, mod func(*IssueCardsInput)) *Card {
		t.Helper()
		in := IssueCardsInput{Kind: kind, Target: usersTarget(u.ID)}
		if mod != nil {
			mod(&in)
		}
		return f.myCards(u.ID, f.issue(in).Batch.ID)[0]
	}
	status := func(c *Card) string {
		t.Helper()
		var s string
		if err := f.pool.QueryRow(f.ctx, `SELECT status FROM reset_cards WHERE id = $1`, c.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	burn := func(subID uuid.UUID, n int, at time.Time) {
		for i := range n {
			f.record(subID, "m", protocol.Usage{}, "0", at.Add(time.Duration(i)*time.Second))
		}
	}
	t0 := f.now.Add(time.Minute)
	burn(sub.ID, 4, t0)
	burn(rsub.ID, 3, t0)
	f.now = t0.Add(time.Hour)
	weeklyStart := t0.Truncate(time.Second)

	// Preview lists the applicable subscriptions with the affected rules.
	c5 := card(CardKind5h, nil)
	pv, err := f.svc.PreviewCard(f.ctx, u.ID, c5.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Subscriptions) != 2 || pv.Subscriptions[0].ID != sub.ID || len(pv.Subscriptions[0].Rules) != 1 ||
		pv.Subscriptions[0].Rules[0].ID != "5h" || pv.Subscriptions[0].Rules[0].Used != "4" || !pv.Subscriptions[0].HasUsage ||
		pv.Subscriptions[1].ID != rsub.ID || pv.Subscriptions[1].Rules[0].ID != "r5" {
		t.Fatalf("preview %+v", pv.Subscriptions)
	}
	if _, err := f.svc.PreviewCard(f.ctx, other.ID, c5.ID); errCode(err) != "not_found" {
		t.Fatalf("preview of someone else's card: %v", err)
	}

	// 5h card: only the 5-hour session is reset (anchored at now).
	res, err := use(c5, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]RuleUsage{}
	for _, r := range res.Subscription.Rules {
		rules[r.ID] = r
	}
	if len(res.Rules) != 1 || res.Rules[0] != "5h" || res.Card.Status != CardUsed || res.Card.UsedAt == nil ||
		res.Card.Subscription == nil || res.Card.Subscription.PlanName != "Pro" ||
		rules["5h"].Used != "0" || !rules["5h"].ResetsAt.Equal(f.now.Truncate(time.Second).Add(5*time.Hour)) ||
		rules["weekly"].Used != "4" || !rules["weekly"].WindowStart.Equal(weeklyStart) || rules["monthly"].Used != "4" {
		t.Fatalf("5h card result %+v %+v", res, rules)
	}
	if _, err := use(c5, sub.ID); errCode(err) != "card_used" {
		t.Fatalf("reuse: %v", err)
	}

	// weekly card: only the weekly session.
	f.now = f.now.Add(time.Minute)
	res, err = use(card(CardKindWeekly, nil), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r := f.rulesOf(u.ID, sub.ID); r["weekly"].Used != "0" || !r["weekly"].ResetsAt.Equal(f.now.Truncate(time.Second).Add(7*day)) ||
		r["5h"].WindowStart.Equal(f.now.Truncate(time.Second)) || res.Rules[0] != "weekly" {
		t.Fatalf("weekly card %+v", r)
	}

	// both: every matching rule with the same anchor; rolling rules lose their buckets.
	f.now = f.now.Add(time.Minute)
	burn(sub.ID, 2, f.now.Add(-30*time.Second))
	res, err = use(card(CardKindBoth, nil), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := f.rulesOf(u.ID, sub.ID)
	anchor := f.now.Truncate(time.Second)
	if len(res.Rules) != 2 || r["5h"].Used != "0" || r["weekly"].Used != "0" || !r["5h"].WindowStart.Equal(anchor) ||
		!r["weekly"].WindowStart.Equal(anchor) || r["monthly"].Used != "6" {
		t.Fatalf("both card %+v", r)
	}
	res, err = use(card(CardKindBoth, nil), rsub.ID)
	if err != nil || len(res.Rules) != 2 {
		t.Fatalf("both card on rolling: %v %+v", err, res)
	}
	if r := f.rulesOf(u.ID, rsub.ID); r["r5"].Used != "0" || r["r7"].Used != "0" || r["r5"].ResetsAt != nil {
		t.Fatalf("rolling after card %+v", r)
	}

	// No matching rule (calendar day, 6h rolling): rejected, card not consumed.
	cw := card(CardKindWeekly, nil)
	if _, err := use(cw, dsub.ID); errCode(err) != "card_not_applicable" || status(cw) != CardAvailable {
		t.Fatalf("not applicable: %v %s", err, status(cw))
	}
	pv, _ = f.svc.PreviewCard(f.ctx, u.ID, cw.ID)
	if len(pv.Subscriptions) != 2 { // Pro and Roll, not Daily
		t.Fatalf("weekly preview %+v", pv.Subscriptions)
	}

	// Plan restriction.
	cr := card(CardKind5h, func(in *IssueCardsInput) { in.PlanIDs = []uuid.UUID{rolling.ID} })
	if _, err := use(cr, sub.ID); errCode(err) != "card_plan_not_allowed" || status(cr) != CardAvailable {
		t.Fatalf("plan restriction: %v", err)
	}
	if pv, _ = f.svc.PreviewCard(f.ctx, u.ID, cr.ID); len(pv.Subscriptions) != 1 || pv.Subscriptions[0].ID != rsub.ID {
		t.Fatalf("restricted preview %+v", pv.Subscriptions)
	}
	if _, err := use(cr, rsub.ID); err != nil {
		t.Fatalf("allowed plan: %v", err)
	}

	// Someone else's subscription or card; unknown ids; inactive subscription.
	if _, err := use(cw, osub.ID); errCode(err) != "not_found" || status(cw) != CardAvailable {
		t.Fatalf("other's subscription: %v", err)
	}
	if _, err := use(cw, uuid.New()); errCode(err) != "not_found" {
		t.Fatalf("unknown subscription: %v", err)
	}
	if _, err := f.svc.UseCard(f.ctx, other, cw.ID, osub.ID, RequestMeta{}); errCode(err) != "not_found" || status(cw) != CardAvailable {
		t.Fatalf("other's card: %v", err)
	}
	if _, err := f.svc.UseCard(f.ctx, u, uuid.New(), sub.ID, RequestMeta{}); errCode(err) != "not_found" {
		t.Fatalf("unknown card: %v", err)
	}
	if _, err := f.svc.Cancel(f.ctx, f.user(identity.RoleSystemAdmin), rsub.ID, "", RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := use(cw, rsub.ID); errCode(err) != "subscription_not_active" || status(cw) != CardAvailable {
		t.Fatalf("cancelled subscription: %v", err)
	}

	// Expired and revoked cards.
	ce := card(CardKind5h, func(in *IssueCardsInput) { in.ExpiresAt = ptr(f.now.Add(time.Hour)) })
	f.now = f.now.Add(time.Hour)
	if _, err := use(ce, sub.ID); errCode(err) != "card_expired" {
		t.Fatalf("expired: %v", err)
	}
	if c := f.myCards(u.ID, ce.BatchID)[0]; c.Status != CardExpired {
		t.Fatalf("expired status %s", c.Status)
	}
	if pv, _ = f.svc.PreviewCard(f.ctx, u.ID, ce.ID); len(pv.Subscriptions) != 0 {
		t.Fatalf("expired preview %+v", pv)
	}
	if _, err := f.svc.RevokeCardBatch(f.ctx, f.user(identity.RoleSystemAdmin), cw.BatchID, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := use(cw, sub.ID); errCode(err) != "card_revoked" {
		t.Fatalf("revoked: %v", err)
	}

	// Usable cards come first in the user's list.
	mine, _ := f.svc.ListMyCards(f.ctx, u.ID)
	if len(mine.Items) == 0 || mine.Items[0].Status == CardAvailable || mine.Available[CardKind5h] != 0 {
		t.Fatalf("my cards %+v", mine.Available)
	}
	fresh := card(CardKind5h, nil)
	if mine, _ = f.svc.ListMyCards(f.ctx, u.ID); mine.Items[0].ID != fresh.ID || mine.Available[CardKind5h] != 1 {
		t.Fatalf("usable first: %+v", mine.Items[0])
	}
	var audits int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE action = 'reset_card.use'`).Scan(&audits)
	if audits != 5 {
		t.Fatalf("use audits %d", audits)
	}
}

func TestCardConcurrentUse(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("10")}})
	sub, _ := f.grant(u.ID, p.ID, 1)
	f.record(sub.ID, "m", protocol.Usage{}, "0", f.now)
	c := f.myCards(u.ID, f.issue(IssueCardsInput{Kind: CardKindBoth, Target: usersTarget(u.ID)}).Batch.ID)[0]
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, used := 0, 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.UseCard(f.ctx, u, c.ID, sub.ID, RequestMeta{})
			mu.Lock()
			defer mu.Unlock()
			switch errCode(err) {
			case "":
				ok++
			case "card_used":
				used++
			default:
				t.Errorf("concurrent use: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || used != 7 {
		t.Fatalf("succeeded %d, card_used %d", ok, used)
	}
	var audits int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE action = 'reset_card.use'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("use audits %d", audits)
	}
}

func TestCardHTTP(t *testing.T) {
	f := newFixture(t)
	h := newHTTP(t, f)
	admin, user := f.user(identity.RoleSystemAdmin), f.user(identity.RoleUser)
	const A, U = identity.RoleSystemAdmin, identity.RoleUser
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("10")}})
	sub, _ := f.grant(user.ID, p.ID, 1)
	f.record(sub.ID, "m", protocol.Usage{}, "0", f.now)

	body := map[string]any{"kind": "5h", "quantity": 2, "expiresAt": nil, "planIds": []string{p.ID.String()}, "note": "",
		"target": map[string]any{"type": "users", "userIds": []string{user.ID.String()}}}
	if st, _ := h.do(user, U, "POST", "/api/admin/billing/reset-cards/batches", body); st != 403 {
		t.Fatalf("user issuing: %d", st)
	}
	st, out := h.do(admin, A, "POST", "/api/admin/billing/reset-cards/batches?dryRun=true", body)
	if st != 200 || out["recipients"] != float64(1) || out["cards"] != float64(2) || out["batch"] != nil {
		t.Fatalf("dry run: %d %v", st, out)
	}
	for _, bad := range []map[string]any{
		{"kind": "5h", "quantity": 1, "target": map[string]any{"type": "users", "userIds": []string{"x"}}},
		{"kind": "5h", "quantity": 1, "target": map[string]any{"type": "all", "bogus": 1}},
		{"kind": "5h", "quantity": 1, "planIds": []string{"x"}, "target": map[string]any{"type": "all"}},
		{"kind": "5h", "quantity": 1},
	} {
		if st, out := h.do(admin, A, "POST", "/api/admin/billing/reset-cards/batches", bad); st != 422 {
			t.Fatalf("invalid issue %v: %d %v", bad, st, out)
		}
	}
	st, out = h.do(admin, A, "POST", "/api/admin/billing/reset-cards/batches", body)
	if st != 201 || out["batch"].(map[string]any)["counts"].(map[string]any)["available"] != float64(2) {
		t.Fatalf("issue: %d %v", st, out)
	}
	batchID := out["batch"].(map[string]any)["id"].(string)
	if st, out := h.do(admin, A, "GET", "/api/admin/billing/reset-cards/batches", nil); st != 200 || out["total"] != float64(1) {
		t.Fatalf("batches: %d %v", st, out)
	}
	if st, out := h.do(admin, A, "GET", "/api/admin/billing/reset-cards?userId="+user.ID.String(), nil); st != 200 || out["total"] != float64(2) {
		t.Fatalf("user's cards: %d %v", st, out)
	}

	st, out = h.do(user, U, "GET", "/api/billing/reset-cards", nil)
	if st != 200 || len(out["items"].([]any)) != 2 || out["available"].(map[string]any)["5h"] != float64(2) {
		t.Fatalf("my cards: %d %v", st, out)
	}
	cardID := out["items"].([]any)[0].(map[string]any)["id"].(string)
	st, out = h.do(user, U, "GET", "/api/billing/reset-cards/"+cardID+"/preview", nil)
	if st != 200 || len(out["subscriptions"].([]any)) != 1 || out["now"] == nil {
		t.Fatalf("preview: %d %v", st, out)
	}
	if st, _ := h.do(user, U, "POST", "/api/billing/reset-cards/"+cardID+"/use", map[string]any{}); st != 422 {
		t.Fatalf("use without subscription: %d", st)
	}
	st, out = h.do(user, U, "POST", "/api/billing/reset-cards/"+cardID+"/use", map[string]any{"subscriptionId": sub.ID})
	if st != 200 || out["card"].(map[string]any)["status"] != "used" || out["rules"].([]any)[0] != "5h" ||
		out["subscription"].(map[string]any)["rules"].([]any)[0].(map[string]any)["used"] != "0" {
		t.Fatalf("use: %d %v", st, out)
	}
	if st, out := h.do(user, U, "POST", "/api/billing/reset-cards/"+cardID+"/use", map[string]any{"subscriptionId": sub.ID}); st != 409 {
		t.Fatalf("reuse: %d %v", st, out)
	}
	if st, _ := h.do(user, identity.RoleAuditor, "GET", "/api/billing/reset-cards", nil); st != 403 {
		t.Fatalf("auditor: %d", st)
	}
	st, out = h.do(admin, A, "POST", "/api/admin/billing/reset-cards/batches/"+batchID+"/revoke", nil)
	if c := out["counts"].(map[string]any); st != 200 || c["used"] != float64(1) || c["revoked"] != float64(1) || out["status"] != "revoked" {
		t.Fatalf("revoke: %d %v", st, out)
	}
	if st, _ := h.do(admin, A, "POST", "/api/admin/billing/reset-cards/batches/x/revoke", nil); st != 404 {
		t.Fatalf("revoke bad id: %d", st)
	}
}
