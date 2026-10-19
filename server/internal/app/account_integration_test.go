package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A client holding only an API key reads its balance and plan quotas
// (GET /v1/account) and the OpenAI-compatible legacy billing endpoints.
func TestAccountEndpoints(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "oa", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
	plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
		"name": "测试套餐", "description": "", "duration": "30d", "models": []string{"m1"}, "stackable": false,
		"rules": []map[string]any{{"id": "5h", "label": "5 小时窗口", "meter": "requests", "window": map[string]any{"kind": "session", "duration": "5h"}, "limit": "2"}},
	}, 201)
	adminID := e.userID(e.admin)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": adminID, "planId": plan["id"], "periods": 1}, 201)
	e.credit(adminID, "12.5")
	_, key := e.key(e.admin, map[string]any{"name": "k", "policy": map[string]any{"spendLimit": map[string]any{"window": "day", "amount": "3"}}})
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1"))); code != 200 {
		t.Fatalf("request = %d %s", code, raw)
	}
	e.app.FlushLogs(context.Background()) // plan usage is recorded asynchronously

	get := func(path, key string) (int, map[string]any, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return resp.StatusCode, m, string(b)
	}

	code, acc, raw := get("/v1/account", key)
	if code != 200 || acc["object"] != "account" {
		t.Fatalf("account = %d %s", code, raw)
	}
	w := acc["wallet"].(map[string]any)
	k := acc["key"].(map[string]any)
	subs := acc["subscriptions"].([]any)
	if w["balance"] != "12.5" || w["available"] != "12.5" || w["currency"] == "" || k["name"] != "k" || k["spent"] == nil ||
		acc["quotaOverflow"] != "block" || len(subs) != 1 || acc["limits"] == nil {
		t.Fatalf("account = %s", raw)
	}
	rule := subs[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if subs[0].(map[string]any)["plan"].(map[string]any)["name"] != "测试套餐" || rule["used"] != "1" || rule["remaining"] != "1" || rule["resetsAt"] == nil {
		t.Fatalf("subscription = %v", subs[0])
	}
	// Never another key's or user's data, nor the key itself.
	if strings.Contains(raw, key) {
		t.Fatal("the key leaked into the response")
	}

	for _, path := range []string{"/v1/dashboard/billing/subscription", "/dashboard/billing/subscription"} {
		code, sub, raw := get(path, key)
		if code != 200 || sub["object"] != "billing_subscription" || fmt.Sprint(sub["hard_limit_usd"]) != "12.5" {
			t.Fatalf("%s = %d %s", path, code, raw)
		}
	}
	if code, u, raw := get("/v1/dashboard/billing/usage?start_date=2026-10-01&end_date=2026-10-31", key); code != 200 || fmt.Sprint(u["total_usage"]) != "0" {
		t.Fatalf("usage = %d %s", code, raw)
	}
	if code, body, _ := get("/v1/account", ""); code != 401 || errCode(body) != "invalid_api_key" {
		t.Fatalf("no key = %d %v", code, body)
	}
}
