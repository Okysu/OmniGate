package app_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayLogsAllRequestsAndSeparatesRejections(t *testing.T) {
	e := setupGateway(t)
	up := newFakeUpstream(t)
	e.channel(e.admin, map[string]any{"name": "log-test", "type": "openai", "baseUrl": up.srv.URL + "/v1", "scope": "global", "models": models("m1")})
	uid := e.userID(e.carol)
	kid, key := e.key(e.carol, map[string]any{"name": "all-requests"})
	ipID, ipKey := e.key(e.carol, map[string]any{"name": "ip-restricted", "policy": map[string]any{"ipAllowlist": []string{"10.0.0.0/8"}}})
	rpmID, rpmKey := e.key(e.carol, map[string]any{"name": "rpm-restricted", "policy": map[string]any{"rpm": 1}})
	e.cfg.CORSOrigins = []string{"*"}
	h := e.app.Handler()
	type expected struct {
		status                     int
		keyID, model, path, method string
	}
	want := map[string]expected{}
	call := func(method, path, secret, keyID, body, model string, status int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		r.Header.Set("Content-Type", "application/json")
		if method == "OPTIONS" {
			r.Header.Set("Origin", "https://client.example.com")
			r.Header.Set("Access-Control-Request-Method", "POST")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		want[w.Header().Get("X-OmniGate-Request-Id")] = expected{status, keyID, model, r.URL.Path, method}
	}
	call("POST", "/v1/chat/completions", key, kid, chatBody, "m1", 200)
	call("POST", "/v1/chat/completions", key, kid, `{"model":"absent","messages":[]}`, "absent", 404)
	call("POST", "/v1/chat/completions", key, kid, `{`, "", 400)
	call("POST", "/v1/chat/completions", ipKey, ipID, chatBody, "", 403)
	call("POST", "/v1/chat/completions", rpmKey, rpmID, chatBody, "m1", 200)
	call("POST", "/v1/chat/completions", rpmKey, rpmID, chatBody, "", 429)
	call("POST", "/v1/chat/completions", "invalid", "", chatBody, "", 401)
	call("POST", "/v1/absent?secret=do-not-store", key, kid, "", "", 400)
	call("GET", "/v1/chat/completions", key, kid, "", "", 405)
	call("POST", "/v1/messages/count_tokens", key, kid, `{"model":"absent"}`, "absent", 404)
	call("GET", "/v1/models", key, kid, "", "", 200)
	call("GET", "/v1/account", key, kid, "", "", 200)
	call("GET", "/v1/dashboard/billing/subscription", key, kid, "", "", 200)
	call("GET", "/dashboard/billing/usage", key, kid, "", "", 200)
	call("OPTIONS", "/v1/chat/completions", "", "", "", "", 204)
	call("GET", "/v1/responses", key, kid, "", "", 400)
	up.setMode("500")
	call("POST", "/v1/chat/completions", key, kid, chatBody, "m1", 502)
	users := e.mustDo(e.admin, "GET", "/api/admin/users?q=carol", nil, 200)
	u := users["items"].([]any)[0].(map[string]any)
	e.mustDo(e.admin, "PATCH", "/api/admin/users/"+uid, map[string]any{"status": "disabled", "disabledReason": "test", "version": u["version"]}, 200)
	call("POST", "/v1/chat/completions", key, kid, chatBody, "", 403)
	if err := e.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	logs := e.mustDo(e.admin, "GET", "/api/logs?pageSize=100", nil, 200)
	if logs["total"].(float64) != float64(len(want)) {
		t.Fatalf("missing/duplicate logs: %v, want %d", logs, len(want))
	}
	var success, rejected, failed, attributed int
	for _, item := range logs["items"].([]any) {
		l := item.(map[string]any)
		x, ok := want[l["requestId"].(string)]
		if !ok {
			t.Fatalf("unexpected/duplicate log: %v", l)
		}
		delete(want, l["requestId"].(string))
		if l["statusCode"].(float64) != float64(x.status) || l["path"] != x.path || l["method"] != x.method || l["model"] != x.model {
			t.Fatalf("log mismatch: %v, want %+v", l, x)
		}
		if x.keyID != "" {
			attributed++
			if l["keyId"] != x.keyID || l["user"].(map[string]any)["id"] != uid {
				t.Fatalf("lost user/key: %v", l)
			}
		} else if l["user"] != nil {
			t.Fatalf("attributed invalid key: %v", l)
		}
		if l["charge"] != "0" || l["quotaCharge"] != "0" {
			t.Fatalf("unexpected charge: %v", l)
		}
		switch {
		case x.status >= 500:
			failed++
		case x.status >= 400:
			rejected++
		default:
			success++
		}
	}
	if len(want) != 0 {
		t.Fatalf("unlogged requests: %v", want)
	}
	for filter, n := range map[string]int{"success": success, "rejected": rejected, "error": failed} {
		out := e.mustDo(e.admin, "GET", "/api/logs?status="+filter, nil, 200)
		if out["total"].(float64) != float64(n) {
			t.Fatalf("%s filter: %v, want %d", filter, out, n)
		}
	}
	byUser := e.mustDo(e.admin, "GET", "/api/logs?userId="+uid, nil, 200)
	if byUser["total"].(float64) != float64(attributed) {
		t.Fatalf("user filter lost rejected requests: %v", byUser)
	}
	summary := e.mustDo(e.admin, "GET", "/api/stats/summary", nil, 200)
	totals := summary["totals"].(map[string]any)
	n := success + rejected + failed
	if totals["requests"].(float64) != float64(n) || totals["success"].(float64) != float64(success) || totals["errors"].(float64) != float64(failed) || totals["rejected"].(float64) != float64(rejected) || totals["failureRate"].(float64) != float64(failed)/float64(n) {
		t.Fatalf("wrong totals: %v", totals)
	}
	for _, group := range []string{"daily", "byModel", "byChannel", "byClient"} {
		var requests, errors, rejects float64
		for _, raw := range summary[group].([]any) {
			row := raw.(map[string]any)
			requests += row["requests"].(float64)
			errors += row["errors"].(float64)
			rejects += row["rejected"].(float64)
		}
		if requests != float64(n) || errors != float64(failed) || rejects != float64(rejected) {
			t.Fatalf("%s breakdown: %v", group, summary[group])
		}
	}
	raw, _ := json.Marshal(logs)
	if strings.Contains(string(raw), "do-not-store") || strings.Contains(string(raw), key) {
		t.Fatal("credentials/query string leaked into logs")
	}
}

func TestLogKindFilterAndKeyUsage(t *testing.T) {
	e := setupGateway(t)
	up := newFakeUpstream(t)
	e.channel(e.admin, map[string]any{"name": "usage-test", "type": "openai", "baseUrl": up.srv.URL + "/v1", "scope": "global", "models": models("m1", "m2")})
	kid, key := e.key(e.carol, map[string]any{"name": "usage"})
	otherID, otherKey := e.key(e.carol, map[string]any{"name": "other"})
	h := e.app.Handler()
	call := func(method, path, secret, body string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	call("POST", "/v1/chat/completions", key, chatBody)
	call("POST", "/v1/chat/completions", key, chatBody)
	call("POST", "/v1/chat/completions", key, `{"model":"m2","messages":[{"role":"user","content":"hi"}]}`)
	call("POST", "/v1/chat/completions", otherKey, chatBody)
	call("GET", "/v1/models", key, "")
	call("GET", "/v1/models", key, "")
	if err := e.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	for kind, n := range map[string]float64{"": 6, "all": 6, "model": 4, "other": 2} {
		out := e.mustDo(e.carol, "GET", "/api/logs?kind="+kind, nil, 200)
		if out["total"].(float64) != n {
			t.Fatalf("kind=%q: %v, want %v", kind, out["total"], n)
		}
	}
	e.mustDo(e.carol, "GET", "/api/logs?kind=bogus", nil, 422)

	for _, rng := range []string{"", "24h", "7d", "30d", "all"} {
		out := e.mustDo(e.carol, "GET", "/api/stats/keys/"+kid+"/usage?range="+rng, nil, 200)
		totals := out["totals"].(map[string]any)
		// Two m1 requests and one m2 request: 10 input + 2 output tokens each.
		if totals["requests"].(float64) != 3 || totals["inputTokens"].(float64) != 30 || totals["outputTokens"].(float64) != 6 || totals["totalTokens"].(float64) != 36 {
			t.Fatalf("range %q totals: %v", rng, totals)
		}
		byModel := out["byModel"].([]any)
		if len(byModel) != 2 || byModel[0].(map[string]any)["model"] != "m1" || byModel[0].(map[string]any)["requests"].(float64) != 2 {
			t.Fatalf("range %q byModel: %v", rng, byModel)
		}
	}
	other := e.mustDo(e.carol, "GET", "/api/stats/keys/"+otherID+"/usage", nil, 200)
	if other["totals"].(map[string]any)["requests"].(float64) != 1 {
		t.Fatalf("other key: %v", other)
	}
	e.mustDo(e.carol, "GET", "/api/stats/keys/"+kid+"/usage?range=1y", nil, 422)
	dave := e.h.newClient()
	dave.login("dave")
	e.mustDo(dave, "GET", "/api/stats/keys/"+kid+"/usage", nil, 404)
	e.mustDo(e.admin, "GET", "/api/stats/keys/"+kid+"/usage", nil, 200)
}
