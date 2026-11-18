package app_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"omnigate/internal/notify/notifytest"
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

func TestAdminForcedEmailEndpoint(t *testing.T) {
	e := setupGateway(t)
	uid := e.userID(e.carol)
	message := map[string]any{"target": map[string]any{"userIds": []string{uid}}, "title": "重要公告", "body": "强制邮件通知", "forceEmail": true, "idempotencyKey": uuid.NewString()}
	e.mustDo(e.carol, http.MethodPost, "/api/admin/notifications", message, 403)
	// Fail before writing an in-app notice when forced mail cannot be queued.
	e.mustDo(e.admin, http.MethodPost, "/api/admin/notifications", message, 422)
	e.expectNotifs(e.carol, "admin.message", 0)
	srv := notifytest.Start(t, "none")
	e.useSMTP(srv)
	for _, path := range []string{"/api/admin/notifications?dryRun=true", "/api/admin/notifications", "/api/admin/notifications"} {
		got := e.mustDo(e.admin, http.MethodPost, path, message, 200)
		if got["emailQueued"] != float64(1) || got["emailSkipped"] != float64(0) {
			t.Fatalf("result=%v", got)
		}
	}
	e.expectNotifs(e.carol, "admin.message", 1)
}

func TestAdminEmailDeliveryQueryEndpoint(t *testing.T) {
	e := setupGateway(t)
	uid := e.userID(e.carol)
	srv := notifytest.Start(t, "none")
	e.useSMTP(srv)
	path := "/api/admin/notifications/email-deliveries"
	e.mustDo(e.carol, http.MethodGet, path, nil, 403)
	for _, query := range []string{"?userId=bad", "?eventId=bad", "?userId=" + uuid.Nil.String(), "?status=unknown"} {
		e.mustDo(e.admin, http.MethodGet, path+query, nil, 422)
	}
	empty := e.mustDo(e.admin, http.MethodGet, path+"?eventId="+uuid.NewString(), nil, 200)
	if empty["total"] != float64(0) || len(empty["items"].([]any)) != 0 {
		t.Fatalf("empty=%v", empty)
	}
	message := map[string]any{"target": map[string]any{"userIds": []string{uid}}, "title": "停用处理通知", "body": "private notification body", "forceEmail": true, "idempotencyKey": uuid.NewString()}
	sent := e.mustDo(e.admin, http.MethodPost, "/api/admin/notifications", message, 200)
	eventID, ok := sent["eventId"].(string)
	if !ok || eventID == "" {
		t.Fatalf("missing eventId: %v", sent)
	}
	result := e.mustDo(e.admin, http.MethodGet, path+"?userId="+uid+"&eventId="+eventID+"&pageSize=1", nil, 200)
	items := result["items"].([]any)
	if result["total"] != float64(1) || len(items) != 1 || result["pageSize"] != float64(1) {
		t.Fatalf("result=%v", result)
	}
	d := items[0].(map[string]any)
	if d["userId"] != uid || d["eventId"] != eventID || d["title"] != message["title"] || d["forceEmail"] != true || d["status"] == nil {
		t.Fatalf("delivery=%v", d)
	}
	for _, field := range []string{"body", "data", "email", "secret"} {
		if _, ok := d[field]; ok {
			t.Fatalf("unexpected field %s", field)
		}
	}
	again := e.mustDo(e.admin, http.MethodPost, "/api/admin/notifications", message, 200)
	if again["eventId"] != eventID || again["alreadySent"] != true {
		t.Fatalf("idempotent=%v", again)
	}
}
