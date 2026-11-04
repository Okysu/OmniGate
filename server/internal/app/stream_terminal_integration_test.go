package app_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Responses-compatible proxies may append Chat's [DONE] marker after
// response.completed. The answer and reported usage are already final then.
func TestGatewayResponsesTerminalBilling(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, terminal string
		subscription, malformed    bool
	}{
		{"chat_wallet", "/v1/chat/completions", chatStream, "[DONE]", false, false},
		{"chat_subscription", "/v1/chat/completions", chatStream, "[DONE]", true, false},
		{"messages_wallet", "/v1/messages", `{"model":"m1","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "event: message_stop", false, false},
		{"messages_subscription", "/v1/messages", `{"model":"m1","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "event: message_stop", true, false},
		{"responses_wallet", "/v1/responses", `{"model":"m1","input":"hi","stream":true}`, "event: response.completed", false, false},
		{"malformed_before_terminal_wallet", "/v1/chat/completions", chatStream, "", false, true},
		{"malformed_before_terminal_subscription", "/v1/chat/completions", chatStream, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, bob := setupTiers(t)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" {
					t.Errorf("upstream path = %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				event := func(name, data string) {
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
				}
				event("response.created", `{"type":"response.created","response":{"id":"resp_test"}}`)
				event("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"m","delta":"hello"}`)
				if tc.malformed {
					// A sentinel alone cannot substitute for response.completed.
					fmt.Fprint(w, "data: [DONE]\n\n")
					return
				}
				event("response.completed", `{"type":"response.completed","response":{"id":"resp_test","status":"completed","usage":{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":80}}}}`)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			t.Cleanup(up.Close)
			e.channel(e.admin, map[string]any{
				"name": "responses proxy", "type": "openai", "baseUrl": pub(up.URL) + "/v1", "scope": "global",
				"config": map[string]any{"supportsResponses": true},
				"models": []map[string]string{{"model": "m1", "upstreamModel": "deepseek", "upstreamProtocol": "responses"}},
			})
			e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{
				"kind": "sell", "model": "m1", "inputPerM": "1000", "outputPerM": "1000", "cacheReadPerM": "100",
			}, 201)
			e.enforceBilling()
			userID := e.userID(bob)
			e.credit(userID, "100")
			if tc.subscription {
				plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
					"name": "quota", "duration": "30d", "models": []string{"m1"},
					"rules": []map[string]any{{"id": "total", "label": "total", "meter": "charge", "window": map[string]any{"kind": "lifetime"}, "limit": "10"}},
				}, 201)
				e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": userID, "planId": plan["id"], "periods": 1}, 201)
			}
			_, key := e.key(bob, map[string]any{"name": "k"})
			status, _, raw := readBody(gwPost(t, context.Background(), e.h.srv.URL, tc.path, key, tc.body))
			if status != 200 || !strings.Contains(raw, "hello") {
				t.Fatalf("response = %d %s", status, raw)
			}
			log := e.lastLog(e.admin, "userId="+userID)
			wantCharge, wantQuota, wantBalance := "0", "0", "100"
			if tc.malformed {
				if log["errorClass"] != "upstream_invalid_response" || !strings.Contains(raw, "upstream_invalid_response") {
					t.Fatalf("unfinished response not rejected: %v %s", log, raw)
				}
			} else {
				if log["errorClass"] != nil || strings.Contains(raw, "upstream_invalid_response") || strings.Count(raw, tc.terminal) != 1 {
					t.Fatalf("completed response rejected or duplicated: %v %s", log, raw)
				}
				u := log["usage"].(map[string]any)
				if u["input"] != float64(20) || u["cacheRead"] != float64(80) || u["output"] != float64(10) || u["estimated"] != false {
					t.Fatalf("reported usage = %v", u)
				}
				// (20 input + 10 output) * 1000/M + 80 cached * 100/M.
				if tc.subscription {
					wantQuota = "0.038"
				} else {
					wantCharge, wantBalance = "0.038", "99.962"
				}
			}
			if log["charge"] != wantCharge || log["quotaCharge"] != wantQuota {
				t.Fatalf("billing = %v", log)
			}
			wallet := e.mustDo(bob, http.MethodGet, "/api/billing/wallet", nil, 200)
			if wallet["balance"] != wantBalance || wallet["reserved"] != "0" {
				t.Fatalf("wallet = %v", wallet)
			}
			if tc.subscription {
				subs := e.mustDo(bob, http.MethodGet, "/api/billing/subscriptions", nil, 200)
				rule := subs["items"].([]any)[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
				if rule["used"] != wantQuota {
					t.Fatalf("quota = %v", rule)
				}
			}
		})
	}
}
