package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/protocol"
	"omnigate/internal/requestlog"
)

// modelUpstream is a minimal OpenAI-compatible upstream that records the
// models it was asked for and fails with a fixed status when status != 0.
type modelUpstream struct {
	srv    *httptest.Server
	hits   atomic.Int64
	status atomic.Int64
	mu     sync.Mutex
	models []string
}

func newModelUpstream(t *testing.T, status int) *modelUpstream {
	u := &modelUpstream{}
	u.status.Store(int64(status))
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		u.mu.Lock()
		u.models = append(u.models, req.Model)
		u.mu.Unlock()
		if s := int(u.status.Load()); s != 0 {
			http.Error(w, `{"error":{"message":"failing on purpose"}}`, s)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, req.Model)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *modelUpstream) lastModel() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.models) == 0 {
		return ""
	}
	return u.models[len(u.models)-1]
}

func chat(model string) string {
	return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model)
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestRouteRules(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	ua, ub, ug, uo := newModelUpstream(t, 0), newModelUpstream(t, 0), newModelUpstream(t, 0), newModelUpstream(t, 0)
	aID := e.platformChannel(map[string]any{"name": "a", "type": "openai", "baseUrl": ua.srv.URL + "/v1", "priority": 10, "models": models("m1")})
	bID := e.platformChannel(map[string]any{"name": "b", "type": "openai", "baseUrl": ub.srv.URL + "/v1", "models": models("m1")})
	gID := e.platformChannel(map[string]any{"name": "g", "type": "openai", "baseUrl": ug.srv.URL + "/v1", "scope": "global", "models": models("m1")})
	otherID := e.platformChannel(map[string]any{"name": "o", "type": "openai", "baseUrl": uo.srv.URL + "/v1", "models": models("other")})
	_, adminKey := e.key(e.admin, map[string]any{"name": "k"})
	_, carolKey := e.key(e.carol, map[string]any{"name": "ck"})
	call := func(key, model string) (int, map[string]any, string) {
		return readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat(model)))
	}

	// Without rules: priority wins.
	if code, _, raw := call(adminKey, "m1"); code != 200 || ua.hits.Load() != 1 {
		t.Fatalf("no rule = %d %s (a hits %d)", code, raw, ua.hits.Load())
	}

	// Target validation.
	resp, out := e.admin.do(http.MethodPost, "/api/admin/routes", map[string]any{"name": "x", "match": map[string]any{"models": []string{"m1"}},
		"targets": []map[string]any{{"channelId": uuid.NewString(), "priority": nil, "weight": nil}}})
	if resp.StatusCode != 422 || errCode(out) != "route_target_invalid" {
		t.Fatalf("unknown target = %d %v", resp.StatusCode, out)
	}
	resp, out = e.admin.do(http.MethodPost, "/api/admin/routes", map[string]any{"name": "x", "match": map[string]any{"models": []string{"m*"}},
		"targets": []map[string]any{{"channelId": otherID}}})
	if resp.StatusCode != 422 || errCode(out) != "route_target_invalid" {
		t.Fatalf("target without the model = %d %v", resp.StatusCode, out)
	}
	if resp, out := e.carol.do(http.MethodGet, "/api/admin/routes", nil); resp.StatusCode != 403 {
		t.Fatalf("user lists routes = %d %v", resp.StatusCode, out)
	}

	// Targets restrict the candidates (and override priority/weight).
	r1 := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes", map[string]any{"name": "only-b", "description": "pin to b",
		"match":   map[string]any{"models": []string{"m*"}, "roles": []string{"system_admin"}},
		"targets": []map[string]any{{"channelId": bID, "priority": 1, "weight": nil}}}, 201)
	if r1["position"].(float64) != 0 || r1["strategy"] != "priority" || r1["protocolPreference"] != "native_first" ||
		r1["retry"].(map[string]any)["maxAttempts"].(float64) != 3 || len(r1["retry"].(map[string]any)["retryOn"].([]any)) != 6 ||
		r1["version"].(float64) != 1 || r1["enabled"] != true {
		t.Fatalf("created rule = %v", r1)
	}
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 3; i++ {
		if code, _, raw := call(adminKey, "m1"); code != 200 {
			t.Fatalf("pinned = %d %s", code, raw)
		}
	}
	if ua.hits.Load() != 1 || ub.hits.Load() != 3 {
		t.Fatalf("targets ignored: a=%d b=%d", ua.hits.Load(), ub.hits.Load())
	}

	// A rule never widens access: carol cannot reach alice's private channel.
	r2 := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes", map[string]any{"name": "users",
		"match":   map[string]any{"models": []string{"m1"}, "roles": []string{"user"}},
		"targets": []map[string]any{{"channelId": aID}, {"channelId": gID}}}, 201)
	if r2["position"].(float64) != 1 {
		t.Fatalf("second rule position = %v", r2["position"])
	}
	for i := 0; i < 3; i++ {
		if code, _, raw := call(carolKey, "m1"); code != 200 {
			t.Fatalf("carol = %d %s", code, raw)
		}
	}
	if ua.hits.Load() != 1 || ug.hits.Load() != 3 {
		t.Fatalf("carol reached a private channel: a=%d g=%d", ua.hits.Load(), ug.hits.Load())
	}
	upd := e.mustDo(e.admin, http.MethodPatch, "/api/admin/routes/"+r2["id"].(string), map[string]any{"version": 1,
		"targets": []map[string]any{{"channelId": aID}}}, 200)
	if upd["version"].(float64) != 2 {
		t.Fatalf("update = %v", upd)
	}
	if code, out, _ := call(carolKey, "m1"); code != 404 || ua.hits.Load() != 1 {
		t.Fatalf("target outside access = %d %v (a=%d)", code, out, ua.hits.Load())
	}
	if resp, out := e.admin.do(http.MethodPatch, "/api/admin/routes/"+r2["id"].(string), map[string]any{"version": 1, "name": "stale"}); resp.StatusCode != 409 {
		t.Fatalf("stale update = %d %v", resp.StatusCode, out)
	}

	// lowest_cost: cost price per channel (input + output), no price last.
	for _, p := range []struct{ ch, in, out string }{{aID, "5", "5"}, {bID, "1", "1"}} {
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "cost", "model": "up-m1", "channelId": p.ch, "inputPerM": p.in, "outputPerM": p.out}, 201)
	}
	r1 = e.mustDo(e.admin, http.MethodPatch, "/api/admin/routes/"+r1["id"].(string), map[string]any{"version": 1, "targets": []any{}, "strategy": "lowest_cost"}, 200)
	time.Sleep(20 * time.Millisecond)
	before := ub.hits.Load()
	for i := 0; i < 3; i++ {
		if code, _, raw := call(adminKey, "m1"); code != 200 {
			t.Fatalf("lowest cost = %d %s", code, raw)
		}
	}
	if ub.hits.Load() != before+3 {
		t.Fatalf("lowest_cost did not pick the cheapest channel (b hits %d → %d)", before, ub.hits.Load())
	}

	// Preview (current user).
	an := e.platformChannel(map[string]any{"name": "an", "type": "anthropic", "baseUrl": uo.srv.URL, "models": models("m1")})
	pv := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "m1", "inbound": "openai.chat"}, 200)
	if rule := pv["rule"].(map[string]any); rule["name"] != "only-b" || pv["strategy"] != "lowest_cost" || pv["maxAttempts"].(float64) != 3 ||
		len(pv["fallbackModels"].([]any)) != 0 {
		t.Fatalf("preview = %v", pv)
	}
	cands := pv["candidates"].([]any)
	var order []string
	for _, c := range cands {
		order = append(order, c.(map[string]any)["channelName"].(string))
	}
	if strings.Join(order[:2], ",") != "b,a" || len(order) != 4 {
		t.Fatalf("preview order = %v", order)
	}
	first := cands[0].(map[string]any)
	if first["channelId"] != bID || first["costPerM"] != "2" || first["breaker"] != "closed" || first["upstreamDialect"] != "openai.chat" ||
		first["conversionHops"].(float64) != 0 || first["skipped"] != nil || first["latencyMs"] == nil || first["channelType"] != "openai" {
		t.Fatalf("preview candidate = %v", first)
	}
	for _, c := range cands[2:] {
		m := c.(map[string]any)
		if m["costPerM"] != nil {
			t.Fatalf("channel without cost price = %v", m)
		}
		if m["channelId"] == an && m["conversionHops"].(float64) != 1 {
			t.Fatalf("anthropic candidate = %v", m)
		}
	}
	// Embeddings cannot use the anthropic channel.
	pv = e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "m1", "inbound": "openai.embeddings"}, 200)
	last := pv["candidates"].([]any)[len(pv["candidates"].([]any))-1].(map[string]any)
	if last["channelId"] != an || last["skipped"] == nil {
		t.Fatalf("unsupported candidate = %v", last)
	}
	// Preview for another user: carol's rule; alice's private target is listed as skipped.
	_, me := e.carol.do(http.MethodGet, "/api/me", nil)
	carolID := me["user"].(map[string]any)["id"].(string)
	pv = e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "m1", "userId": carolID, "inbound": "anthropic.messages"}, 200)
	if pv["rule"].(map[string]any)["name"] != "users" || len(pv["candidates"].([]any)) != 1 {
		t.Fatalf("carol preview = %v", pv)
	}
	if c := pv["candidates"].([]any)[0].(map[string]any); c["channelId"] != aID || c["skipped"] == nil {
		t.Fatalf("carol candidate = %v", c)
	}
	pv = e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "zzz", "inbound": "openai.responses"}, 200)
	if pv["rule"] != nil || pv["strategy"] != "priority" || pv["maxAttempts"].(float64) != 3 || len(pv["candidates"].([]any)) != 0 {
		t.Fatalf("no-rule preview = %v", pv)
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "m1", "inbound": "grpc"}, 422)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "m1", "inbound": "openai.chat", "userId": uuid.NewString()}, 404)

	// Reorder, list, delete.
	id1, id2 := r1["id"].(string), r2["id"].(string)
	e.mustDo(e.admin, http.MethodPut, "/api/admin/routes/order", map[string]any{"ids": []string{id2}}, 422)
	e.mustDo(e.admin, http.MethodPut, "/api/admin/routes/order", map[string]any{"ids": []string{id2, id2}}, 422)
	list := e.mustDo(e.admin, http.MethodPut, "/api/admin/routes/order", map[string]any{"ids": []string{id2, id1}}, 200)
	items := list["items"].([]any)
	if items[0].(map[string]any)["id"] != id2 || items[1].(map[string]any)["position"].(float64) != 1 {
		t.Fatalf("reorder = %v", items)
	}
	e.mustDo(e.admin, http.MethodDelete, "/api/admin/routes/"+id2, nil, 204)
	e.mustDo(e.admin, http.MethodDelete, "/api/admin/routes/"+id2, nil, 404)
	list = e.mustDo(e.admin, http.MethodGet, "/api/admin/routes", nil, 200)
	if items := list["items"].([]any); len(items) != 1 || items[0].(map[string]any)["position"].(float64) != 0 {
		t.Fatalf("after delete = %v", items)
	}
	for _, action := range []string{"route.create", "route.update", "route.delete", "route.reorder"} {
		logs := e.mustDo(e.admin, http.MethodGet, "/api/admin/audit-logs?action="+action, nil, 200)
		if logs["total"].(float64) < 1 {
			t.Errorf("no %s audit entry", action)
		}
	}
}

func TestRouteFallbackAndRetry(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	bad1, bad2, good := newModelUpstream(t, 500), newModelUpstream(t, 500), newModelUpstream(t, 0)
	e.platformChannel(map[string]any{"name": "bad1", "type": "openai", "baseUrl": bad1.srv.URL + "/v1", "models": models("m1")})
	e.platformChannel(map[string]any{"name": "bad2", "type": "openai", "baseUrl": bad2.srv.URL + "/v1", "models": models("m1")})
	e.platformChannel(map[string]any{"name": "good", "type": "openai", "baseUrl": good.srv.URL + "/v1", "models": models("m2")})
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m1", "inputPerM": "1", "outputPerM": "1"}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m2", "inputPerM": "1000", "outputPerM": "2000"}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/routes", map[string]any{"name": "fallback", "match": map[string]any{"models": []string{"m1"}},
		"retry": map[string]any{"maxAttempts": 2}, "fallbackModels": []string{"m2"}}, 201)
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1")))
	if code != 200 || !strings.Contains(raw, `"ok"`) {
		t.Fatalf("fallback = %d %s", code, raw)
	}
	if bad1.hits.Load() != 1 || bad2.hits.Load() != 1 || good.hits.Load() != 1 || good.lastModel() != "up-m2" {
		t.Fatalf("hits bad1=%d bad2=%d good=%d model=%q", bad1.hits.Load(), bad2.hits.Load(), good.hits.Load(), good.lastModel())
	}
	if err := e.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	logs := e.mustDo(e.admin, http.MethodGet, "/api/logs?model=m1", nil, 200)
	entry := logs["items"].([]any)[0].(map[string]any)
	// Charged at m2's price: 10 input × 1000/M + 2 output × 2000/M.
	if entry["model"] != "m1" || entry["servedModel"] != "m2" || entry["charge"] != "0.014" || entry["attempts"].(float64) != 3 ||
		entry["upstreamModel"] != "up-m2" || entry["statusCode"].(float64) != 200 {
		t.Fatalf("fallback log = %v", entry)
	}

	// retryOn without server_error: a 5xx stops after the first channel (no fallback either).
	bad3, bad4 := newModelUpstream(t, 500), newModelUpstream(t, 500)
	e.platformChannel(map[string]any{"name": "bad3", "type": "openai", "baseUrl": bad3.srv.URL + "/v1", "models": models("m3")})
	e.platformChannel(map[string]any{"name": "bad4", "type": "openai", "baseUrl": bad4.srv.URL + "/v1", "models": models("m3")})
	rule := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes", map[string]any{"name": "strict", "match": map[string]any{"models": []string{"m3"}},
		"retry": map[string]any{"maxAttempts": 3, "retryOn": []string{"rate_limit"}}, "fallbackModels": []string{"m2"}}, 201)
	code, _, raw = readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m3")))
	if code != 502 || bad3.hits.Load()+bad4.hits.Load() != 1 || good.hits.Load() != 1 {
		t.Fatalf("non-retryable class = %d %s (hits %d+%d, good %d)", code, raw, bad3.hits.Load(), bad4.hits.Load(), good.hits.Load())
	}
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/routes/"+rule["id"].(string), map[string]any{"version": 1,
		"retry": map[string]any{"retryOn": []string{"server_error"}}, "fallbackModels": []string{}}, 200)
	code, _, _ = readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m3")))
	if code != 502 || bad3.hits.Load()+bad4.hits.Load() != 3 {
		t.Fatalf("retryable class = %d (hits %d+%d)", code, bad3.hits.Load(), bad4.hits.Load())
	}
}

func TestSystemSettings(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	anon := e.h.newClient()

	st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	s := st["settings"].(map[string]any)
	src := st["sources"].(map[string]any)
	ro := st["readonly"].(map[string]any)
	if s["site"].(map[string]any)["name"] != "OmniGate" || s["auth"].(map[string]any)["registrationMode"] != "open" ||
		s["gateway"].(map[string]any)["maxAttempts"].(float64) != 3 || s["gateway"].(map[string]any)["defaultOutputTokens"] != nil ||
		s["gateway"].(map[string]any)["logRetentionDays"].(float64) != 90 || s["billing"].(map[string]any)["signupCredit"] != "0" ||
		s["billing"].(map[string]any)["enforce"] != false || s["site"].(map[string]any)["landingEnabled"] != true ||
		st["version"].(float64) != 1 {
		t.Fatalf("defaults = %v", st)
	}
	if src["site.name"] != "default" || src["auth.registrationMode"] != "env" || len(src) != 24 || src["notifications.smtp.host"] != "default" ||
		src["gateway.affinity"] != "default" || src["billing.referralEnabled"] != "default" ||
		s["billing"].(map[string]any)["referralEnabled"] != false || s["billing"].(map[string]any)["referralRate"] != "10" ||
		s["billing"].(map[string]any)["referralMinRecharge"] != "0" ||
		src["site.publicModelPlaza"] != "default" || s["site"].(map[string]any)["publicModelPlaza"] != true {
		t.Fatalf("sources = %v", src)
	}
	if ro["publicUrl"] != "http://localhost:8080" || ro["env"] != "development" || ro["channelsAllowPrivateNetwork"] != true ||
		len(ro["loginProviders"].([]any)) != 1 || ro["currency"].(map[string]any)["code"] != "USD" {
		t.Fatalf("readonly = %v", ro)
	}
	if resp, _ := e.carol.do(http.MethodGet, "/api/admin/settings", nil); resp.StatusCode != 403 {
		t.Fatalf("user reads settings = %d", resp.StatusCode)
	}

	st = e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": 1, "settings": map[string]any{
		"site":    map[string]any{"name": " Acme AI ", "announcement": "今晚维护", "landingEnabled": false, "docsUrl": "https://docs.example.com"},
		"gateway": map[string]any{"maxAttempts": 1}}}, 200)
	if st["version"].(float64) != 2 || st["settings"].(map[string]any)["site"].(map[string]any)["name"] != "Acme AI" ||
		st["sources"].(map[string]any)["site.name"] != "db" || st["sources"].(map[string]any)["gateway.maxAttempts"] != "db" {
		t.Fatalf("patch = %v", st)
	}
	resp, out := e.admin.do(http.MethodPatch, "/api/admin/settings", map[string]any{"version": 1, "settings": map[string]any{"site": map[string]any{"name": "x"}}})
	if resp.StatusCode != 409 || errCode(out) != "version_conflict" {
		t.Fatalf("stale = %d %v", resp.StatusCode, out)
	}
	resp, out = e.admin.do(http.MethodPatch, "/api/admin/settings", map[string]any{"version": 2, "settings": map[string]any{
		"site": map[string]any{"docsUrl": "ftp://x"}, "gateway": map[string]any{"maxAttempts": 9, "defaultOutputTokens": 4096},
		"billing": map[string]any{"signupCredit": "-1"}, "nope": map[string]any{"x": 1}}})
	details, _ := out["error"].(map[string]any)["details"].(map[string]any)
	if resp.StatusCode != 422 || len(details) != 5 || details["site.docsUrl"] == nil || details["nope.x"] == nil ||
		details["gateway.defaultOutputTokens"] == nil {
		t.Fatalf("invalid = %d %v", resp.StatusCode, out)
	}

	_, info := anon.do(http.MethodGet, "/api/system/info", nil)
	if info["siteName"] != "Acme AI" || info["announcement"] != "今晚维护" || info["landingEnabled"] != false ||
		info["docsUrl"] != "https://docs.example.com" || info["registrationMode"] != "open" || info["name"] != "OmniGate" {
		t.Fatalf("system info = %v", info)
	}

	// gateway.maxAttempts applies to requests without a route rule.
	b1, b2 := newModelUpstream(t, 500), newModelUpstream(t, 500)
	// (Platform channels: carol calls alice's global channels.)
	e.channel(e.admin, map[string]any{"name": "b1", "type": "openai", "baseUrl": b1.srv.URL + "/v1", "scope": "global", "models": models("mx")})
	e.channel(e.admin, map[string]any{"name": "b2", "type": "openai", "baseUrl": b2.srv.URL + "/v1", "scope": "global", "models": models("mx")})
	_, key := e.key(e.carol, map[string]any{"name": "k"})
	if code, _, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("mx"))); code != 502 || b1.hits.Load()+b2.hits.Load() != 1 {
		t.Fatalf("maxAttempts=1: %d (hits %d+%d)", code, b1.hits.Load(), b2.hits.Load())
	}

	// null resets to env / default.
	st = e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": 2, "settings": map[string]any{
		"site": map[string]any{"name": nil}, "gateway": map[string]any{"maxAttempts": nil}}}, 200)
	if st["version"].(float64) != 3 || st["settings"].(map[string]any)["site"].(map[string]any)["name"] != "OmniGate" ||
		st["sources"].(map[string]any)["site.name"] != "default" || st["sources"].(map[string]any)["site.announcement"] != "db" {
		t.Fatalf("reset = %v", st)
	}
	audit := e.mustDo(e.admin, http.MethodGet, "/api/admin/audit-logs?action=settings.update", nil, 200)
	newest := audit["items"].([]any)[0].(map[string]any)["metadata"].(map[string]any)
	if audit["total"].(float64) != 2 || newest["before"].(map[string]any)["site.name"] != "Acme AI" ||
		newest["after"].(map[string]any)["site.name"] != "OmniGate" || newest["after"].(map[string]any)["gateway.maxAttempts"].(float64) != 3 {
		t.Fatalf("audit = %v", audit)
	}

	// billing.enforce keeps its old endpoint as an alias.
	bs := e.mustDo(e.admin, http.MethodGet, "/api/admin/billing/settings", nil, 200)
	if bs["enforce"] != false || bs["version"].(float64) != 3 {
		t.Fatalf("billing settings alias = %v", bs)
	}
	e.mustDo(e.admin, http.MethodPut, "/api/admin/billing/settings", map[string]any{"enforce": true, "version": 2}, 409)
	bs = e.mustDo(e.admin, http.MethodPut, "/api/admin/billing/settings", map[string]any{"enforce": true, "version": 3}, 200)
	if bs["enforce"] != true || bs["version"].(float64) != 4 {
		t.Fatalf("billing settings put = %v", bs)
	}
	st = e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	if st["settings"].(map[string]any)["billing"].(map[string]any)["enforce"] != true || st["sources"].(map[string]any)["billing.enforce"] != "db" {
		t.Fatalf("enforce via settings = %v", st)
	}

	// Registration mode from settings overrides the environment.
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": 4, "settings": map[string]any{
		"auth": map[string]any{"registrationMode": "closed"}}}, 200)
	if loc := e.h.newClient().login("bob"); loc != "/login?error=registration_closed" {
		t.Fatalf("closed registration = %q", loc)
	}
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": 5, "settings": map[string]any{
		"auth": map[string]any{"registrationMode": "restricted", "allowedEmailDomains": []string{"example.net"}}}}, 200)
	if loc := e.h.newClient().login("bob"); loc != "/login?error=not_allowed" {
		t.Fatalf("restricted, other domain = %q", loc)
	}
	_, info = anon.do(http.MethodGet, "/api/system/info", nil)
	if info["registrationMode"] != "restricted" {
		t.Fatalf("system info registration mode = %v", info["registrationMode"])
	}

	// Sign-up credit on first login (bob@other.org is a verified email).
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": 6, "settings": map[string]any{
		"auth": map[string]any{"allowedEmailDomains": []string{"Other.org"}}, "billing": map[string]any{"signupCredit": "5.50"}}}, 200)
	bob := e.h.newClient()
	if loc := bob.login("bob"); loc != "/admin/users" {
		t.Fatalf("bob login = %q", loc)
	}
	again := e.h.newClient()
	again.login("bob")
	wallet := e.mustDo(bob, http.MethodGet, "/api/billing/wallet", nil, 200)
	if wallet["balance"] != "5.5" {
		t.Fatalf("bob wallet = %v", wallet)
	}
	ledger := e.mustDo(bob, http.MethodGet, "/api/billing/ledger", nil, 200)
	if items := ledger["items"].([]any); len(items) != 1 || items[0].(map[string]any)["kind"] != "grant" || items[0].(map[string]any)["refType"] != "signup" {
		t.Fatalf("bob ledger = %v", ledger)
	}
	if a := e.mustDo(e.admin, http.MethodGet, "/api/admin/audit-logs?action=billing.signup_credit", nil, 200); a["total"].(float64) != 1 {
		t.Fatalf("signup credit audit = %v", a)
	}
	// Existing users get nothing.
	if w := e.mustDo(e.carol, http.MethodGet, "/api/billing/wallet", nil, 200); w["balance"] != "0" {
		t.Fatalf("carol wallet = %v", w)
	}
}

func TestRequestLogRetention(t *testing.T) {
	dsn := testDB(t)
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	month := func(y int, m time.Month) time.Time { return time.Date(y, m, 15, 0, 0, 0, 0, time.UTC) }
	for _, at := range []time.Time{month(2026, 5), month(2026, 7), month(2026, 10)} {
		if err := requestlog.EnsurePartitions(ctx, pool, at); err != nil { // creates that month and the next
			t.Fatal(err)
		}
	}
	rows := map[string]bool{ // started_at → kept
		"2026-01-10T00:00:00Z": false, // default partition, old
		"2026-05-20T00:00:00Z": false,
		"2026-06-30T00:00:00Z": false,
		"2026-07-05T00:00:00Z": false, // boundary partition, before the cutoff
		"2026-07-20T00:00:00Z": true,
		"2026-09-15T00:00:00Z": true, // default partition, recent
		"2026-10-01T00:00:00Z": true,
	}
	for ts := range rows {
		at, _ := time.Parse(time.RFC3339, ts)
		if _, err := pool.Exec(ctx, `INSERT INTO request_logs (id, started_at, request_id, inbound, status_code, duration_ms)
			VALUES ($1, $2, 'r', 'openai.chat', 200, 1)`, uuid.New(), at); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) // 90 days → cutoff 2026-07-10 12:00
	if res, err := requestlog.Prune(ctx, pool, now, 0); err != nil || len(res.DroppedPartitions) != 0 || res.DeletedRows != 0 {
		t.Fatalf("retention 0 = %+v %v", res, err)
	}
	res, err := requestlog.Prune(ctx, pool, now, 90)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM request_logs`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("remaining = %d %v", n, err)
	}
	if pool.Dialect() == db.SQLite { // one table: every old row is deleted
		if len(res.DroppedPartitions) != 0 || res.DeletedRows != 4 {
			t.Fatalf("sqlite prune = %+v", res)
		}
		return
	}
	if fmt.Sprint(res.DroppedPartitions) != "[request_logs_y2026m05 request_logs_y2026m06]" && fmt.Sprint(res.DroppedPartitions) != "[request_logs_y2026m06 request_logs_y2026m05]" {
		t.Fatalf("dropped = %v", res.DroppedPartitions)
	}
	if res.DeletedRows != 2 {
		t.Fatalf("deleted rows = %d", res.DeletedRows)
	}
	var reg *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('request_logs_y2026m07')::text`).Scan(&reg); err != nil || reg == nil {
		t.Fatalf("boundary partition dropped: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('request_logs_y2026m05')::text`).Scan(&reg); err != nil || reg != nil {
		t.Fatalf("old partition still exists: %v", err)
	}
}

// TestGatewayPrepaidReservation covers the prepaid hold: input estimate plus
// max_tokens only when the request sets it, capped at the available balance;
// only an available balance ≤ 0 rejects.
func TestGatewayPrepaidReservation(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "oa", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m1", "inputPerM": "1000", "outputPerM": "1000"}, 201)
	st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"billing": map[string]any{"enforce": true}}}, 200)
	_, me := e.admin.do(http.MethodGet, "/api/me", nil)
	adminID := me["user"].(map[string]any)["id"].(string)
	adjust := func(amount string) {
		w := e.mustDo(e.admin, http.MethodGet, "/api/admin/billing/wallets/"+adminID, nil, 200)
		e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/wallets/"+adminID+"/adjust", map[string]any{"amount": amount, "note": "test", "version": w["version"]}, 200)
	}
	wallet := func() map[string]any { return e.mustDo(e.admin, http.MethodGet, "/api/billing/wallet", nil, 200) }
	_, key := e.key(e.admin, map[string]any{"name": "k"})
	price := money.MustParse("1000")
	cost := func(tokens int64) string { c, _ := money.TokenCost(price, tokens); return c.String() }
	actual, _ := money.TokenCost(price, 12) // fake upstream usage: 10 prompt + 2 completion tokens

	// (c) nothing available → 402.
	if code, out, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1"))); code != 402 {
		t.Fatalf("empty wallet = %d %v", code, out)
	}

	// (a) without max_tokens only the input estimate is held.
	adjust("10")
	up.setMode("block")
	body := `{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp := gwPost(t, context.Background(), base, "/v1/chat/completions", key, body)
	if w := wallet(); w["reserved"] != cost(protocol.EstimateTokens(len(body))) {
		t.Fatalf("reserved without max_tokens = %v, want %s", w["reserved"], cost(protocol.EstimateTokens(len(body))))
	}
	up.release <- struct{}{}
	readBody(resp)
	if err := e.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := money.MustParse("10") - actual
	if w := wallet(); w["reserved"] != "0" || w["balance"] != want.String() {
		t.Fatalf("after settle = %v, want balance %s", w, want)
	}

	// (b) a huge max_tokens with a small balance is admitted; the hold is capped
	// at the available balance and settlement charges the actual cost.
	adjust((money.MustParse("0.5") - want).String()) // balance 0.5
	body = `{"model":"m1","stream":true,"max_tokens":1000000,"messages":[{"role":"user","content":"hi"}]}`
	resp = gwPost(t, context.Background(), base, "/v1/chat/completions", key, body)
	if resp.StatusCode != 200 {
		t.Fatalf("huge max_tokens = %d", resp.StatusCode)
	}
	if w := wallet(); w["reserved"] != "0.5" || w["available"] != "0" {
		t.Fatalf("capped hold = %v", w)
	}
	up.release <- struct{}{}
	readBody(resp)
	if err := e.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	want = money.MustParse("0.5") - actual
	if w := wallet(); w["reserved"] != "0" || w["balance"] != want.String() {
		t.Fatalf("after capped settle = %v, want %s", w, want)
	}

	// (c) again: once the balance is used up, requests are rejected.
	adjust((-want).String())
	up.setMode("ok")
	if code, out, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1"))); code != 402 {
		t.Fatalf("zero balance = %d %v", code, out)
	}
}

// Without a route rule the gateway.retryOn setting decides; other 4xx are
// retried on another channel only when client_error is enabled.
func TestDefaultRetryOnClientError(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	bad, good := newModelUpstream(t, 400), newModelUpstream(t, 0)
	e.channel(e.admin, map[string]any{"name": "bad", "type": "openai", "baseUrl": bad.srv.URL + "/v1", "models": models("m4"), "priority": 10})
	e.channel(e.admin, map[string]any{"name": "good", "type": "openai", "baseUrl": good.srv.URL + "/v1", "models": models("m4")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m4")))
	if code != 400 || bad.hits.Load() != 1 || good.hits.Load() != 0 {
		t.Fatalf("default (no client_error) = %d %s hits %d/%d", code, raw, bad.hits.Load(), good.hits.Load())
	}
	st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	if n := len(st["settings"].(map[string]any)["gateway"].(map[string]any)["retryOn"].([]any)); n != 6 {
		t.Fatalf("default retryOn has %d classes", n)
	}
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"gateway": map[string]any{"retryOn": []string{"rate_limit", "server_error", "timeout", "network", "auth_error", "not_found", "client_error"}}}}, 200)
	if resp, out := e.admin.do(http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"].(float64) + 1, "settings": map[string]any{
		"gateway": map[string]any{"retryOn": []string{"4xx"}}}}); resp.StatusCode != 422 {
		t.Fatalf("invalid class = %d %v", resp.StatusCode, out)
	}
	code, _, raw = readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m4")))
	if code != 200 || bad.hits.Load() != 2 || good.hits.Load() != 1 {
		t.Fatalf("client_error enabled = %d %s hits %d/%d", code, raw, bad.hits.Load(), good.hits.Load())
	}
}

func TestRoutePreviewImagesInbound(t *testing.T) {
	e := setupGateway(t)
	oa, an := newModelUpstream(t, 0), newModelUpstream(t, 0)
	e.channel(e.admin, map[string]any{"name": "oa", "type": "openai", "baseUrl": oa.srv.URL + "/v1", "models": models("img")})
	e.channel(e.admin, map[string]any{"name": "an", "type": "anthropic", "baseUrl": an.srv.URL, "models": models("img")})
	out := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "img", "inbound": "openai.images.generations"}, 200)
	skipped := map[string]any{}
	for _, c := range out["candidates"].([]any) {
		m := c.(map[string]any)
		skipped[m["channelName"].(string)] = m["skipped"]
	}
	if skipped["oa"] != nil || skipped["an"] == nil || !strings.Contains(skipped["an"].(string), "OpenAI") {
		t.Fatalf("images preview = %v", out["candidates"])
	}
}
