package subscription

import (
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
