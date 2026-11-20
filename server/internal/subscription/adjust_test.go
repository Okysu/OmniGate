package subscription

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"omnigate/internal/identity"
	"omnigate/internal/protocol"
)

func TestSyncOldPlanLimitsPreservesUsageAndWindows(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("10")}})
	v, _ := f.grant(u.ID, p.ID, 1)
	start := f.now.Add(time.Minute)
	f.record(v.ID, "m", protocol.Usage{}, "0", start)
	f.now = start.Add(time.Hour)
	before := f.rulesOf(u.ID, v.ID)["5h"]
	rules := []Rule{sessionRule("20")}
	archived := PlanArchived
	updated, err := f.svc.UpdatePlan(f.ctx, f.user(identity.RoleSystemAdmin), p.ID, PlanPatch{Version: p.Version, Rules: &rules, Status: &archived}, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.rulesOf(u.ID, v.ID)["5h"]; got.Limit != "10" {
		t.Fatal("limit changed without opt-in")
	}
	synced, err := f.svc.UpdatePlan(f.ctx, f.user(identity.RoleSystemAdmin), p.ID, PlanPatch{Version: updated.Version, SyncSubscriptionLimits: true}, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	got := f.rulesOf(u.ID, v.ID)["5h"]
	if got.Limit != "20" || got.Used != "1" || got.Remaining != "19" || !got.WindowStart.Equal(*before.WindowStart) || !got.ResetsAt.Equal(*before.ResetsAt) {
		t.Fatalf("sync %+v", got)
	}
	current, err := getSub(f.ctx, f.pool, v.ID, false)
	if err != nil || !current.EndsAt.Equal(v.EndsAt) {
		t.Fatalf("expiry %v %v", current, err)
	}
	// A capacity sync must not silently migrate a legacy window or partially
	// save the plan when any subscription's matching rule is incompatible.
	incompatible := []Rule{sessionRule("30")}
	incompatible[0].Window.Duration = "7d"
	if _, err := f.svc.UpdatePlan(f.ctx, f.user(identity.RoleSystemAdmin), p.ID, PlanPatch{Version: synced.Version, Rules: &incompatible, SyncSubscriptionLimits: true}, RequestMeta{}); err == nil {
		t.Fatal("incompatible window accepted")
	}
	if got := f.rulesOf(u.ID, v.ID)["5h"]; got.Limit != "20" || !got.ResetsAt.Equal(*before.ResetsAt) {
		t.Fatalf("incompatible sync changed subscription %+v", got)
	}
	stored, err := f.svc.GetPlan(f.ctx, p.ID)
	if err != nil || stored.Version != synced.Version || stored.Rules[0].Limit != "20" {
		t.Fatalf("incompatible sync saved plan %+v %v", stored, err)
	}
}

func TestSyncModelWeightsOnlyChangesFutureUsage(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	rule := Rule{ID: "total", Meter: MeterCharge, Limit: "24", Window: Window{Kind: WindowLifetime}}
	p := f.plan(PlanInput{Rules: []Rule{rule}})
	v, _ := f.grant(u.ID, p.ID, 1)
	f.record(v.ID, "flash", protocol.Usage{}, "4", f.now)
	rule.Limit = "66"
	rule.ModelWeights = map[string]string{"flash": "0.5"}
	rules := []Rule{rule}
	_, err := f.svc.UpdatePlan(f.ctx, f.user(identity.RoleSystemAdmin), p.ID, PlanPatch{Version: p.Version, Rules: &rules, SyncSubscriptionWeights: true}, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	f.record(v.ID, "flash", protocol.Usage{}, "4", f.now.Add(time.Minute))
	got := f.rulesOf(u.ID, v.ID)["total"]
	if got.Limit != "24" || got.Used != "6" || got.Remaining != "18" {
		t.Fatalf("old subscription after weighted usage: %+v", got)
	}
}

func TestAdjustQuotaPreservesRefreshAndIsAtomic(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Stackable: true, Rules: []Rule{sessionRule("10"), rollingRule("roll", "5h", "10"), {ID: "total", Meter: MeterRequests, Limit: "10", Window: Window{Kind: WindowLifetime}}}})
	v, _ := f.grant(u.ID, p.ID, 1)
	start := f.now.Add(time.Minute)
	f.record(v.ID, "m", protocol.Usage{}, "0", start)
	f.record(v.ID, "m", protocol.Usage{}, "0", start.Add(time.Minute))
	f.now = start.Add(time.Hour)
	actor := f.user(identity.RoleSystemAdmin)
	target := Target{IDs: []uuid.UUID{v.ID}}
	for _, id := range []string{"5h", "roll", "total"} {
		before := f.rulesOf(u.ID, v.ID)[id]
		in := AdjustQuotaInput{Target: target, RuleID: id, Limit: ptr("20"), Remaining: ptr("17"), Note: "容量调整"}
		if _, err := f.svc.AdjustQuota(f.ctx, actor, in, true, RequestMeta{}); err != nil {
			t.Fatal(err)
		}
		if got := f.rulesOf(u.ID, v.ID)[id]; got.Limit != "10" || got.Used != "2" {
			t.Fatalf("dry run mutated %+v", got)
		}
		if _, err := f.svc.AdjustQuota(f.ctx, actor, in, false, RequestMeta{}); err != nil {
			t.Fatal(err)
		}
		got := f.rulesOf(u.ID, v.ID)[id]
		if got.Limit != "20" || got.Used != "3" || got.Remaining != "17" || !got.WindowStart.Equal(*before.WindowStart) || !sameTime(got.ResetsAt, before.ResetsAt) {
			t.Fatalf("adjust %s %+v", id, got)
		}
		in.Limit = nil
		in.Remaining = ptr("19")
		if _, err := f.svc.AdjustQuota(f.ctx, actor, in, false, RequestMeta{}); err != nil {
			t.Fatal(err)
		}
		if got := f.rulesOf(u.ID, v.ID)[id]; got.Used != "1" || !sameTime(got.ResetsAt, before.ResetsAt) {
			t.Fatalf("reduce %s %+v", id, got)
		}
	}
	idle, _ := f.grant(u.ID, p.ID, 1)
	in := AdjustQuotaInput{Target: Target{IDs: []uuid.UUID{v.ID, idle.ID}}, RuleID: "5h", Limit: ptr("30"), Remaining: ptr("1")}
	if _, err := f.svc.AdjustQuota(f.ctx, actor, in, false, RequestMeta{}); err == nil {
		t.Fatal("started idle session")
	}
	if got := f.rulesOf(u.ID, v.ID)["5h"]; got.Limit != "20" || got.Used != "1" {
		t.Fatalf("partial batch committed %+v", got)
	}
}

func sameTime(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}

func TestSyncMissingRulesPreservesUsageAndExpiry(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	total := Rule{ID: "total", Meter: MeterCharge, Limit: "24", Window: Window{Kind: WindowLifetime}, ModelWeights: map[string]string{"flash": "0.5"}}
	p := f.plan(PlanInput{Rules: []Rule{total, sessionRule("10")}})
	v, _ := f.grant(u.ID, p.ID, 1)
	f.record(v.ID, "flash", protocol.Usage{}, "4", f.now)
	before := f.rulesOf(u.ID, v.ID)["5h"]
	hydrogen := Rule{ID: "hydrogen", Meter: MeterCharge, Limit: "12", Window: Window{Kind: WindowLifetime}, Models: []string{"hydrogen"}}
	rules := []Rule{total, sessionRule("20"), hydrogen}
	admin := f.user(identity.RoleSystemAdmin)
	updated, err := f.svc.UpdatePlan(f.ctx, admin, p.ID, PlanPatch{Version: p.Version, Rules: &rules}, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.rulesOf(u.ID, v.ID)["hydrogen"]; ok {
		t.Fatal("added without opt-in")
	}
	updated, err = f.svc.UpdatePlan(f.ctx, admin, p.ID, PlanPatch{Version: updated.Version, SyncSubscriptionRules: true}, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	got := f.rulesOf(u.ID, v.ID)
	if got["total"].Used != "2" || got["hydrogen"].Used != "0" || got["5h"].Limit != "10" || !sameTime(got["5h"].WindowStart, before.WindowStart) || !sameTime(got["5h"].ResetsAt, before.ResetsAt) {
		t.Fatalf("initial sync: %+v", got)
	}
	f.record(v.ID, "hydrogen", protocol.Usage{}, "3", f.now.Add(time.Minute))
	f.record(v.ID, "flash", protocol.Usage{}, "2", f.now.Add(2*time.Minute))
	_, err = f.svc.UpdatePlan(f.ctx, admin, p.ID, PlanPatch{Version: updated.Version, SyncSubscriptionRules: true}, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	got = f.rulesOf(u.ID, v.ID)
	if got["total"].Used != "6" || got["hydrogen"].Used != "3" || got["hydrogen"].Remaining != "9" {
		t.Fatalf("repeat sync: %+v", got)
	}
	sub, err := getSub(f.ctx, f.pool, v.ID, false)
	if err != nil || len(sub.Rules) != 3 || !sub.StartsAt.Equal(v.StartsAt) || !sub.EndsAt.Equal(v.EndsAt) {
		t.Fatalf("snapshot: %+v %v", sub, err)
	}
}

func TestSyncMissingRulesOverflowRollsBackPlanAndModels(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	rules := make([]Rule, maxRules)
	for i := range rules {
		rules[i] = Rule{ID: fmt.Sprintf("old%d", i), Meter: MeterCharge, Limit: "24", Window: Window{Kind: WindowLifetime}}
	}
	p := f.plan(PlanInput{Rules: rules})
	v, _ := f.grant(u.ID, p.ID, 1)
	next := []Rule{{ID: "new", Meter: MeterCharge, Limit: "12", Window: Window{Kind: WindowLifetime}}}
	models := []string{"hydrogen"}
	_, err := f.svc.UpdatePlan(f.ctx, f.user(identity.RoleSystemAdmin), p.ID, PlanPatch{Version: p.Version, Rules: &next, Models: &models, SyncSubscriptionRules: true, SyncSubscriptionModels: true}, RequestMeta{})
	if err == nil {
		t.Fatal("accepted oversized merged snapshot")
	}
	stored, _ := f.svc.GetPlan(f.ctx, p.ID)
	sub, _ := getSub(f.ctx, f.pool, v.ID, false)
	if stored.Version != p.Version || len(stored.Rules) != maxRules || len(sub.Rules) != maxRules || !reflect.DeepEqual(sub.Models, v.Models) {
		t.Fatal("failed sync partially committed")
	}
}

func TestSyncMissingRulesAndModelsIncludesFutureSubscriptions(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := f.plan(PlanInput{Rules: []Rule{sessionRule("10")}})
	v, _ := f.grant(u.ID, p.ID, 1)
	starts := f.now.Add(time.Hour)
	if _, err := f.pool.Exec(f.ctx, "UPDATE subscriptions SET starts_at = $2 WHERE id = $1", v.ID, starts); err != nil {
		t.Fatal(err)
	}
	rules := []Rule{sessionRule("10"), {ID: "hydrogen", Meter: MeterCharge, Limit: "12", Window: Window{Kind: WindowLifetime}, Models: []string{"hydrogen"}}}
	models := []string{"hydrogen"}
	_, err := f.svc.UpdatePlan(f.ctx, f.user(identity.RoleSystemAdmin), p.ID, PlanPatch{Version: p.Version, Rules: &rules, Models: &models, SyncSubscriptionRules: true, SyncSubscriptionModels: true}, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := getSub(f.ctx, f.pool, v.ID, false)
	if err != nil || len(sub.Rules) != 2 || !reflect.DeepEqual(sub.Models, models) || !sub.StartsAt.Equal(starts) || !sub.EndsAt.Equal(v.EndsAt) {
		t.Fatalf("future snapshot: %+v %v", sub, err)
	}
}
