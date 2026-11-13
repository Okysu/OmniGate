package subscription

import (
	"encoding/json"
	"github.com/google/uuid"
	"omnigate/internal/identity"
	"testing"
	"time"
)

func TestAudienceSharedSelectionAndSubscriptionSnapshot(t *testing.T) {
	f := newFixture(t)
	admin := f.user(identity.RoleSystemAdmin)
	a, b, c, d := f.user(identity.RoleUser), f.user(identity.RoleUser), f.user(identity.RoleUser), f.user(identity.RoleUser)
	formal := f.plan(PlanInput{Name: "Stackable formal", Stackable: true, Rules: []Rule{weeklySession("100")}})
	excluded := f.plan(PlanInput{Name: "Explicitly excluded", Rules: []Rule{weeklySession("100")}})
	other := f.plan(PlanInput{Name: "No weekly window", Rules: []Rule{sessionRule("10")}})
	f.grant(a.ID, formal.ID, 1)
	f.grant(a.ID, formal.ID, 1)
	f.grant(a.ID, excluded.ID, 1)
	f.grant(b.ID, excluded.ID, 1)
	f.grant(c.ID, other.ID, 1)
	f.grant(c.ID, excluded.ID, 1)
	sd, _ := f.grant(d.ID, formal.ID, 1)
	if _, err := f.pool.Exec(f.ctx, "UPDATE subscriptions SET starts_at=$2 WHERE id=$1", sd.ID, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Editing current plan rules must not change eligibility of old snapshots.
	raw, _ := json.Marshal([]Rule{sessionRule("10")})
	if _, err := f.pool.Exec(f.ctx, "UPDATE plans SET rules=$2 WHERE id=$1", formal.ID, raw); err != nil {
		t.Fatal(err)
	}
	filter := AudienceFilter{RequireActiveSubscription: true, QuotaKind: CardKindWeekly, ExcludePlanIDs: []uuid.UUID{excluded.ID}}
	preview, err := PreviewAudience(f.ctx, f.pool, filter, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Users != 1 || preview.Subscriptions != 2 || preview.Sample[0].ID != a.ID {
		t.Fatalf("preview=%+v", preview)
	}
	subs, err := f.svc.targetSubs(f.ctx, f.pool, Target{Filter: &filter}, f.now, false)
	if err != nil || len(subs) != 2 {
		t.Fatalf("bulk=%v %v", subs, err)
	}
	cards, err := f.svc.IssueCards(f.ctx, admin, IssueCardsInput{Kind: CardKindWeekly, Quantity: 2, Target: CardTarget{Type: CardTargetFilter, Filter: &filter}}, true, RequestMeta{})
	if err != nil || cards.Recipients != preview.Users {
		t.Fatalf("cards=%+v %v", cards, err)
	}
	users, _, err := AudienceUsers(f.ctx, f.pool, filter, f.now, []uuid.UUID{b.ID})
	if err != nil || len(users) != 0 {
		t.Fatal("excluded-only user eligible", err)
	}
	users, _, err = AudienceUsers(f.ctx, f.pool, AudienceFilter{}, f.now, []uuid.UUID{})
	if err != nil || len(users) != 0 {
		t.Fatal("empty intersection broadened", err)
	}
	// A weekly window on another subscription must not satisfy selected plan.
	filter.PlanIDs = []uuid.UUID{other.ID}
	preview, err = PreviewAudience(f.ctx, f.pool, filter, f.now)
	if err != nil || preview.Users != 0 {
		t.Fatalf("cross-subscription match: %+v %v", preview, err)
	}
	var n int
	_ = f.pool.QueryRow(f.ctx, "SELECT count(*) FROM reset_cards").Scan(&n)
	if n != 0 {
		t.Fatal("preview mutated cards")
	}
	// Actual quota reset selects exactly the same eligible subscription instances.
	filter.PlanIDs = nil
	result, err := f.svc.ResetQuota(f.ctx, admin, ResetInput{Target: Target{Filter: &filter}}, false, RequestMeta{})
	if err != nil || result.Affected != 2 {
		t.Fatalf("reset=%+v %v", result, err)
	}
}

func TestAudienceHTTPPermissionAndMalformedFilters(t *testing.T) {
	f := newFixture(t)
	h := newHTTP(t, f)
	admin := f.user(identity.RoleSystemAdmin)
	u := f.user(identity.RoleUser)
	status, _ := h.do(u, identity.RoleUser, "POST", "/api/admin/billing/audience/preview", AudienceFilter{})
	if status != 403 {
		t.Fatalf("permission=%d", status)
	}
	status, out := h.do(admin, identity.RoleSystemAdmin, "POST", "/api/admin/billing/audience/preview", AudienceFilter{UserIDs: []uuid.UUID{u.ID}})
	if status != 200 || out["users"] != float64(1) {
		t.Fatalf("preview=%d %v", status, out)
	}
	for _, body := range []any{map[string]any{"target": map[string]any{"filter": map[string]any{"excludePlanId": []string{}}}}, map[string]any{"target": map[string]any{"filter": nil}}, map[string]any{"target": map[string]any{"filter": map[string]any{"quotaKind": "invalid"}}}, map[string]any{"target": map[string]any{"filter": map[string]any{}, "ids": []string{}}}} {
		status, _ = h.do(admin, identity.RoleSystemAdmin, "POST", "/api/admin/billing/subscriptions/reset-quota", body)
		if status != 422 && status != 400 {
			t.Fatalf("invalid filter=%d", status)
		}
	}
}

func TestAudienceVerifiedPaidCodeDoesNotFabricatePurchases(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	other := f.user(identity.RoleUser)
	filter := AudienceFilter{RequirePurchase: true, VerifiedPurchaseUserIDs: []uuid.UUID{u.ID}}
	preview, err := PreviewAudience(f.ctx, f.pool, filter, f.now)
	if err != nil || preview.Users != 1 || preview.Sample[0].ID != u.ID {
		t.Fatalf("verified paid=%+v %v", preview, err)
	}
	recipients, _, err := AudienceUsers(f.ctx, f.pool, filter, f.now, []uuid.UUID{other.ID})
	if err != nil || len(recipients) != 0 {
		t.Fatal("unverified user qualified", err)
	}
	var n int
	_ = f.pool.QueryRow(f.ctx, "SELECT count(*) FROM plan_purchases").Scan(&n)
	if n != 0 {
		t.Fatal("fabricated purchase history")
	}
	filter.RequirePurchase = false
	if err = filter.Validate(); err == nil {
		t.Fatal("unused paid override accepted")
	}
}
