package app_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Round 11 (docs/contracts/phase11-api.md): quota reset cards end to end —
// issuing (dry run, notification in-app and by email), using a card on a
// blocked subscription through the API and the gateway admitting again.
func TestResetCards(t *testing.T) {
	e, smtp := eventsEnv(t)
	base := e.h.srv.URL
	ctx := context.Background()
	up := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "plat", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("c1")})
	plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
		"name": "卡测试", "description": "", "duration": "30d", "models": []string{"c1"},
		"rules": []map[string]any{
			{"id": "5h", "label": "5 小时", "meter": "requests", "window": map[string]any{"kind": "session", "duration": "5h"}, "limit": "1"},
			{"id": "weekly", "label": "每周", "meter": "requests", "window": map[string]any{"kind": "session", "duration": "7d"}, "limit": "100"},
		},
	}, 201)
	carolID := e.userID(e.carol)
	sub := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": carolID, "planId": plan["id"], "periods": 1}, 201)
	subID := sub["id"].(string)
	_, carolKey := e.key(e.carol, map[string]any{"name": "c"})
	call := func() int {
		code, _, _ := readBody(gwPost(t, ctx, base, "/v1/chat/completions", carolKey, `{"model":"c1","messages":[{"role":"user","content":"hi"}]}`))
		e.settle()
		return code
	}
	if code := call(); code != 200 {
		t.Fatalf("first request = %d", code)
	}
	if code := call(); code != 429 {
		t.Fatalf("second request = %d, want 429 (5h limit 1)", code)
	}

	issue := map[string]any{"kind": "5h", "quantity": 2, "planIds": []string{plan["id"].(string)}, "note": "节日福利",
		"target": map[string]any{"type": "plan", "planId": plan["id"]}}
	dry := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/reset-cards/batches?dryRun=true", issue, 200)
	if dry["recipients"] != float64(1) || dry["cards"] != float64(2) {
		t.Fatalf("dry run = %v", dry)
	}
	e.expectNotifs(e.carol, "reset_card.issued", 0)
	smtp.Reset()
	res := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/reset-cards/batches", issue, 201)
	batch := res["batch"].(map[string]any)
	if batch["target"].(map[string]any)["planName"] != "卡测试" || batch["counts"].(map[string]any)["available"] != float64(2) {
		t.Fatalf("batch = %v", batch)
	}
	n := e.expectNotifs(e.carol, "reset_card.issued", 1)[0]
	if !strings.Contains(n["title"].(string), "2 张5小时重置卡") || !strings.Contains(n["body"].(string), "节日福利") ||
		!strings.Contains(n["body"].(string), "仅限套餐：卡测试") {
		t.Fatalf("notification = %v", n)
	}
	found := false
	for _, m := range smtp.Wait(1, 3*time.Second) {
		if m.To[0] == "carol@example.com" && strings.Contains(m.Subject, "重置卡") {
			found = true
		}
	}
	if !found {
		t.Fatal("no reset card email to carol")
	}
	if n, items := e.auditCount("reset_card.issue"); n != 1 || !strings.Contains(stringify(items[0]), `"recipients":1`) {
		t.Fatalf("audit = %v", items)
	}

	// Carol previews and uses a card: the 5-hour session restarts, the
	// weekly one keeps its usage, and the gateway admits again.
	mine := e.mustDo(e.carol, http.MethodGet, "/api/billing/reset-cards", nil, 200)
	cardID := mine["items"].([]any)[0].(map[string]any)["id"].(string)
	pv := e.mustDo(e.carol, http.MethodGet, "/api/billing/reset-cards/"+cardID+"/preview", nil, 200)
	targets := pv["subscriptions"].([]any)
	if len(targets) != 1 || targets[0].(map[string]any)["id"] != subID || targets[0].(map[string]any)["hasUsage"] != true {
		t.Fatalf("preview = %v", pv)
	}
	used := e.mustDo(e.carol, http.MethodPost, "/api/billing/reset-cards/"+cardID+"/use", map[string]any{"subscriptionId": subID}, 200)
	if fmt.Sprint(used["rules"]) != "[5h]" {
		t.Fatalf("use = %v", used)
	}
	rules := e.expectUsed(e.carol, subID, map[string]string{"5h": "0", "weekly": "1"})
	if rules["5h"]["windowStart"] == nil || rules["5h"]["resetsAt"] == nil {
		t.Fatalf("5h after card = %v", rules["5h"])
	}
	if code := call(); code != 200 {
		t.Fatalf("request after card = %d", code)
	}
	e.expectUsed(e.carol, subID, map[string]string{"5h": "1", "weekly": "2"})
	e.mustDo(e.carol, http.MethodPost, "/api/billing/reset-cards/"+cardID+"/use", map[string]any{"subscriptionId": subID}, 409)
	if n, _ := e.auditCount("reset_card.use"); n != 1 {
		t.Fatalf("use audits = %d", n)
	}

	// Revoking the batch voids the remaining card.
	rv := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/reset-cards/batches/"+batch["id"].(string)+"/revoke", nil, 200)
	if c := rv["counts"].(map[string]any); c["used"] != float64(1) || c["revoked"] != float64(1) {
		t.Fatalf("revoke = %v", rv)
	}
	mine = e.mustDo(e.carol, http.MethodGet, "/api/billing/reset-cards", nil, 200)
	if mine["available"].(map[string]any)["5h"] != float64(0) {
		t.Fatalf("cards after revoke = %v", mine)
	}
}
