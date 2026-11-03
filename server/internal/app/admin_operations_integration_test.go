package app_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestAdminPersonalNotificationAndQuotaEndpoints(t *testing.T) {
	e := setupGateway(t)
	uid := e.userID(e.carol)
	message := map[string]any{"target": map[string]any{"userIds": []string{uid, uid}}, "title": "感谢支持", "body": "福利已到账！", "idempotencyKey": uuid.NewString()}
	if got := e.mustDo(e.admin, http.MethodPost, "/api/admin/notifications?dryRun=true", message, 200); got["recipients"] != float64(1) {
		t.Fatalf("preview %v", got)
	}
	e.expectNotifs(e.carol, "admin.message", 0)
	e.mustDo(e.carol, http.MethodPost, "/api/admin/notifications", message, 403)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/notifications", message, 200)
	if got := e.mustDo(e.admin, http.MethodPost, "/api/admin/notifications", message, 200); got["alreadySent"] != true {
		t.Fatalf("repeat %v", got)
	}
	e.expectNotifs(e.carol, "admin.message", 1)
	plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{"name": "legacy", "duration": "30d", "models": []string{"m"}, "rules": []map[string]any{{"id": "weekly", "meter": "requests", "limit": "10", "window": map[string]any{"kind": "session", "duration": "7d"}}}}, 201)
	sub := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": uid, "planId": plan["id"], "periods": 1}, 201)
	adjust := map[string]any{"target": map[string]any{"ids": []any{sub["id"]}}, "ruleId": "weekly", "limit": "20"}
	e.mustDo(e.carol, http.MethodPost, "/api/admin/billing/subscriptions/adjust-quota", adjust, 403)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions/adjust-quota?dryRun=true", adjust, 200)
	if got := e.ruleUsage(e.carol, sub["id"].(string))["weekly"]; got["limit"] != "10" {
		t.Fatalf("dry mutated %v", got)
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions/adjust-quota", adjust, 200)
	if got := e.ruleUsage(e.carol, sub["id"].(string))["weekly"]; got["limit"] != "20" || got["windowStart"] != nil || got["resetsAt"] != nil {
		t.Fatalf("adjust idle %v", got)
	}
	// Plan edits can explicitly synchronize capacities into already-granted snapshots.
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/billing/plans/"+plan["id"].(string), map[string]any{"version": plan["version"], "syncSubscriptionLimits": true, "rules": []map[string]any{{"id": "weekly", "meter": "requests", "limit": "30", "window": map[string]any{"kind": "session", "duration": "7d"}}}}, 200)
	if got := e.ruleUsage(e.carol, sub["id"].(string))["weekly"]; got["limit"] != "30" || got["resetsAt"] != nil {
		t.Fatalf("sync %v", got)
	}
}
