package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recUpstream is an OpenAI Chat (and, on /v1/messages, Anthropic) upstream
// that records what it receives and reports prompt cache hits (80 of 100
// prompt tokens).
type recUpstream struct {
	srv  *httptest.Server
	fail atomic.Bool
	mu   sync.Mutex
	reqs []recRequest
}

type recRequest struct {
	header http.Header
	body   map[string]any
}

func newRecUpstream(t *testing.T) *recUpstream {
	u := &recUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		u.reqs = append(u.reqs, recRequest{header: r.Header.Clone(), body: body})
		u.mu.Unlock()
		if u.fail.Load() {
			http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/messages" {
			fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
				"usage":{"input_tokens":20,"output_tokens":5,"cache_read_input_tokens":80}}`)
			return
		}
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":80}}}`)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *recUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.reqs)
}

func (u *recUpstream) last() recRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqs[len(u.reqs)-1]
}

// affinityEnv is two platform channels serving m1 behind a round-robin route
// rule: without affinity, consecutive requests alternate between them.
type affinityEnv struct {
	*gwEnv
	key   string
	up    map[string]*recUpstream // by channel id
	names map[string]string       // channel id → name
}

func setupAffinity(t *testing.T) *affinityEnv {
	e := setupGateway(t)
	a := &affinityEnv{gwEnv: e, up: map[string]*recUpstream{}, names: map[string]string{}}
	for _, name := range []string{"ch-a", "ch-b"} {
		u := newRecUpstream(t)
		id := e.platformChannel(map[string]any{"name": name, "type": "openai", "baseUrl": u.srv.URL + "/v1", "models": models("m1"),
			"config": map[string]any{"headers": map[string]string{"Originator": "channel-originator"}}})
		a.up[id], a.names[id] = u, name
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/routes", map[string]any{"name": "rr", "match": map[string]any{"models": []string{"m1"}},
		"strategy": "round_robin"}, 201)
	time.Sleep(50 * time.Millisecond)
	_, a.key = e.key(e.admin, map[string]any{"name": "k"})
	return a
}

// respond sends a Codex-style /v1/responses request; session = "" sends no
// session identifier. It returns the status and the channel that received the
// request (by upstream hit counts).
func (a *affinityEnv) respond(session string, headers ...string) (int, string) {
	a.t.Helper()
	before := map[string]int{}
	for id, u := range a.up {
		before[id] = u.count()
	}
	body := `{"model":"m1","input":"hi"}`
	if session != "" {
		body = `{"model":"m1","input":"hi","prompt_cache_key":"` + session + `"}`
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, a.h.srv.URL+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	served := ""
	for id, u := range a.up {
		if u.count() > before[id] && (!u.fail.Load()) {
			served = id
		}
	}
	return resp.StatusCode, served
}

func (a *affinityEnv) setAffinity(cfg map[string]any) {
	a.t.Helper()
	st := a.mustDo(a.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	a.mustDo(a.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"],
		"settings": map[string]any{"gateway": map[string]any{"affinity": cfg}}}, 200)
}

func (a *affinityEnv) setStatus(id, status string) {
	a.t.Helper()
	ch := a.mustDo(a.ops, http.MethodGet, "/api/channels/"+id, nil, 200)
	a.mustDo(a.ops, http.MethodPatch, "/api/channels/"+id, map[string]any{"status": status, "version": ch["version"]}, 200)
	time.Sleep(60 * time.Millisecond) // registry reloads asynchronously
}

func (a *affinityEnv) other(id string) string {
	for k := range a.up {
		if k != id {
			return k
		}
	}
	return ""
}

func TestSessionAffinityRouting(t *testing.T) {
	a := setupAffinity(t)

	// The default setting ships the presets (enabled, prefer).
	st := a.mustDo(a.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	aff := st["settings"].(map[string]any)["gateway"].(map[string]any)["affinity"].(map[string]any)
	if aff["enabled"] != true || aff["session_mode"] != "prefer" || len(aff["rules"].([]any)) != 3 || st["sources"].(map[string]any)["gateway.affinity"] != "default" {
		t.Fatalf("default affinity = %v", aff)
	}

	// Without a session identifier round robin alternates, and no session
	// header is passed (no rule applies).
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		code, ch := a.respond("", "X-Codex-Turn-Metadata", "{}", "User-Agent", "codex_cli_rs/0.50")
		if code != 200 {
			t.Fatalf("no session = %d", code)
		}
		seen[ch]++
		if h := a.up[ch].last().header; h.Get("X-Codex-Turn-Metadata") != "" || !strings.HasPrefix(h.Get("User-Agent"), "OmniGate/") {
			t.Fatalf("headers passed without a rule: %v", h)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("round robin did not alternate: %v", seen)
	}

	// New sessions spread over both channels; each then sticks to its channel.
	bound := map[string]string{}
	spread := map[string]bool{}
	for i := 1; i <= 4; i++ {
		s := fmt.Sprintf("sess-%d", i)
		code, ch := a.respond(s)
		if code != 200 || ch == "" {
			t.Fatalf("%s = %d", s, code)
		}
		bound[s], spread[ch] = ch, true
	}
	if len(spread) != 2 {
		t.Fatalf("sessions did not spread: %v", bound)
	}
	for round := 0; round < 3; round++ {
		for s, want := range bound {
			if code, ch := a.respond(s); code != 200 || ch != want {
				t.Fatalf("round %d: %s went to %s, bound to %s", round, s, a.names[ch], a.names[want])
			}
		}
	}

	// Session headers reach the upstream; the client's User-Agent replaces the
	// gateway's; the channel's own header wins (keep_origin); credentials and
	// cookies never pass. prompt_cache_key survives Responses → Chat.
	code, ch := a.respond("sess-1", "Session_id", "sess-1", "X-Codex-Turn-Metadata", `{"turn":2}`, "User-Agent", "codex_cli_rs/0.50",
		"Originator", "codex_cli_rs", "Cookie", "sid=secret")
	if code != 200 || ch != bound["sess-1"] {
		t.Fatalf("headers request = %d on %s", code, a.names[ch])
	}
	got := a.up[ch].last()
	h := got.header
	if h.Get("Session_id") != "sess-1" || h.Get("X-Codex-Turn-Metadata") != `{"turn":2}` || h.Get("User-Agent") != "codex_cli_rs/0.50" {
		t.Fatalf("session headers not passed: %v", h)
	}
	if h.Get("Originator") != "channel-originator" {
		t.Fatalf("keep_origin: Originator = %q", h.Get("Originator"))
	}
	if h.Get("Cookie") != "" || h.Get("Authorization") != "Bearer sk-upstream-secret-0123456789" {
		t.Fatalf("forbidden headers forwarded: %v", h)
	}
	if got.body["prompt_cache_key"] != "sess-1" {
		t.Fatalf("prompt_cache_key dropped in conversion: %v", got.body)
	}
	// A request identified only by the Session_id header is pinned too.
	code, first := a.respond("", "Session_id", "header-only")
	for i := 0; i < 3; i++ {
		if c, ch := a.respond("", "Session_id", "header-only"); code != 200 || c != 200 || ch != first {
			t.Fatalf("header session moved: %s → %s", a.names[first], a.names[ch])
		}
	}

	// Prefer: the bound channel fails → fail over and rebind.
	x := bound["sess-2"]
	y := a.other(x)
	a.up[x].fail.Store(true)
	if code, ch := a.respond("sess-2"); code != 200 || ch != y {
		t.Fatalf("prefer failover = %d on %s", code, a.names[ch])
	}
	a.up[x].fail.Store(false)
	if code, ch := a.respond("sess-2"); code != 200 || ch != y {
		t.Fatalf("not rebound: %d on %s", code, a.names[ch])
	}

	// Strict (rule session_mode): the bound channel's error is returned
	// without trying the other channel.
	rules := aff["rules"].([]any)
	rules[1].(map[string]any)["session_mode"] = "strict" // codex cli trace ("gpt session" only matches gpt-*)
	a.setAffinity(map[string]any{"enabled": true, "session_mode": "prefer", "switch_on_success": true, "keep_on_channel_disabled": false,
		"max_entries": 1000, "default_ttl_seconds": 600, "rules": rules})
	time.Sleep(20 * time.Millisecond)
	z := bound["sess-3"]
	a.up[z].fail.Store(true)
	otherHits := a.up[a.other(z)].count()
	if code, _ := a.respond("sess-3"); code < 500 {
		t.Fatalf("strict failure = %d", code)
	}
	if a.up[a.other(z)].count() != otherHits {
		t.Fatal("strict mode failed over")
	}
	a.up[z].fail.Store(false)
	if code, ch := a.respond("sess-3"); code != 200 || ch != z {
		t.Fatalf("strict keeps the binding: %d on %s", code, a.names[ch])
	}

	// The bound channel disabled → routed normally, binding replaced.
	w := bound["sess-4"]
	a.setStatus(w, "disabled")
	if code, ch := a.respond("sess-4"); code != 200 || ch != a.other(w) {
		t.Fatalf("disabled bound channel = %d on %s", code, a.names[ch])
	}
	a.setStatus(w, "enabled")
	for i := 0; i < 2; i++ {
		if code, ch := a.respond("sess-4"); code != 200 || ch != a.other(w) {
			t.Fatalf("binding not replaced: %d on %s", code, a.names[ch])
		}
	}

	// keep_on_channel_disabled: the session returns once the channel is back.
	a.setAffinity(map[string]any{"enabled": true, "keep_on_channel_disabled": true, "max_entries": 1000, "default_ttl_seconds": 600, "rules": rules})
	time.Sleep(20 * time.Millisecond)
	_, k := a.respond("sess-keep")
	a.setStatus(k, "disabled")
	if code, ch := a.respond("sess-keep"); code != 200 || ch != a.other(k) {
		t.Fatalf("keep: disabled = %d on %s", code, a.names[ch])
	}
	a.setStatus(k, "enabled")
	if code, ch := a.respond("sess-keep"); code != 200 || ch != k {
		t.Fatalf("keep: session did not return: %d on %s", code, a.names[ch])
	}

	// Request logs: outcome and rule, filterable; never the session value.
	if err := a.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	counts := map[string]float64{}
	for _, o := range []string{"any", "hit", "new", "rebound", "broken", "strict_failed", "failover", "miss", "off"} {
		out := a.mustDo(a.admin, http.MethodGet, "/api/logs?pageSize=200&affinity="+o, nil, 200)
		counts[o] = out["total"].(float64)
		for _, it := range out["items"].([]any) {
			l := it.(map[string]any)
			if o != "any" && l["affinity"] != o || l["affinityRule"] != "codex cli trace" {
				t.Fatalf("log %s: %v / %v", o, l["affinity"], l["affinityRule"])
			}
		}
	}
	if counts["new"] < 6 || counts["hit"] < 15 || counts["rebound"] != 1 || counts["strict_failed"] != 1 || counts["broken"] != 2 || counts["miss"] != 0 || counts["off"] != 0 {
		t.Fatalf("affinity outcomes = %v", counts)
	}
	all := a.mustDo(a.admin, http.MethodGet, "/api/logs?pageSize=200", nil, 200)
	if counts["any"] != all["total"].(float64)-4 { // the four requests without a session
		t.Fatalf("affinity=any = %v of %v", counts["any"], all["total"])
	}
	var leaked int
	if err := a.pool.QueryRow(context.Background(), `SELECT count(*) FROM request_logs WHERE affinity_rule LIKE '%sess-%' OR affinity LIKE '%sess-%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("session value logged: %d %v", leaked, err)
	}
	if resp, _ := a.admin.do(http.MethodGet, "/api/logs?affinity=bogus", nil); resp.StatusCode != 422 {
		t.Fatalf("bad affinity filter = %d", resp.StatusCode)
	}

	// Cache hit metrics: 80 of 100 prompt tokens were cache reads.
	sum := a.mustDo(a.admin, http.MethodGet, "/api/stats/summary", nil, 200)
	totals := sum["totals"].(map[string]any)
	if totals["cacheHitRate"].(float64) < 0.79 || totals["cacheHitRate"].(float64) > 0.81 || totals["cacheReadTokens"].(float64) <= 0 {
		t.Fatalf("totals cache = %v", totals)
	}
	for _, it := range sum["byChannel"].([]any) {
		c := it.(map[string]any)
		if c["channelId"] != nil && (c["cacheHitRate"].(float64) < 0.79 || c["cacheHitRate"].(float64) > 0.81) {
			t.Fatalf("byChannel cache = %v", c)
		}
	}
	if sum["affinity"].(map[string]any)["hit"].(float64) != counts["hit"] {
		t.Fatalf("summary affinity = %v", sum["affinity"])
	}

	// Binding stats and clearing (settings.read / settings.write, audited).
	stats := a.mustDo(a.admin, http.MethodGet, "/api/admin/affinity/stats", nil, 200)
	if stats["entries"].(float64) < 5 || stats["maxEntries"].(float64) != 1000 || stats["rules"].(map[string]any)["codex cli trace"].(float64) != stats["entries"].(float64) {
		t.Fatalf("stats = %v", stats)
	}
	if resp, _ := a.carol.do(http.MethodGet, "/api/admin/affinity/stats", nil); resp.StatusCode != 403 {
		t.Fatalf("carol stats = %d", resp.StatusCode)
	}
	if resp, _ := a.carol.do(http.MethodPost, "/api/admin/affinity/clear", map[string]any{}); resp.StatusCode != 403 {
		t.Fatalf("carol clear = %d", resp.StatusCode)
	}
	cl := a.mustDo(a.admin, http.MethodPost, "/api/admin/affinity/clear", map[string]any{"rule": "claude cli trace"}, 200)
	if cl["cleared"].(float64) != 0 {
		t.Fatalf("clear other rule = %v", cl)
	}
	cl = a.mustDo(a.admin, http.MethodPost, "/api/admin/affinity/clear", map[string]any{"rule": "codex cli trace"}, 200)
	if cl["cleared"].(float64) != stats["entries"].(float64) || cl["stats"].(map[string]any)["entries"].(float64) != 0 {
		t.Fatalf("clear = %v", cl)
	}
	a.mustDo(a.admin, http.MethodPost, "/api/admin/affinity/clear", nil, 200)
	audit := a.mustDo(a.admin, http.MethodGet, "/api/admin/audit-logs?action=affinity.clear", nil, 200)
	if audit["total"].(float64) != 3 {
		t.Fatalf("clear audit = %v", audit["total"])
	}

	// Validation errors name the field; new-api's internal types are refused.
	resp, out := a.admin.do(http.MethodPatch, "/api/admin/settings", map[string]any{"version": a.mustDo(a.admin, http.MethodGet, "/api/admin/settings", nil, 200)["version"],
		"settings": map[string]any{"gateway": map[string]any{"affinity": map[string]any{"rules": []any{map[string]any{"name": "x",
			"key_sources": []any{map[string]any{"type": "context_int", "key": "user_id"}}}}}}}})
	msg, _ := out["error"].(map[string]any)["details"].(map[string]any)["gateway.affinity"].(string)
	if resp.StatusCode != 422 || !strings.Contains(msg, "rules[0].key_sources[0].type") {
		t.Fatalf("invalid setting = %d %v", resp.StatusCode, out)
	}
	// Reset to the default (presets).
	st = a.mustDo(a.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	st = a.mustDo(a.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{"gateway": map[string]any{"affinity": nil}}}, 200)
	if st["sources"].(map[string]any)["gateway.affinity"] != "default" {
		t.Fatalf("reset source = %v", st["sources"])
	}
}

// Disabled affinity: no pinning, no headers, nothing logged.
func TestSessionAffinityDisabled(t *testing.T) {
	a := setupAffinity(t)
	cfg := map[string]any{"enabled": false}
	a.setAffinity(cfg)
	time.Sleep(20 * time.Millisecond)
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		code, ch := a.respond("same", "Session_id", "same")
		if code != 200 {
			t.Fatalf("disabled = %d", code)
		}
		seen[ch] = true
		if a.up[ch].last().header.Get("Session_id") != "" {
			t.Fatal("headers passed while disabled")
		}
	}
	if len(seen) != 2 {
		t.Fatalf("disabled affinity still pinned: %v", seen)
	}
	if err := a.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out := a.mustDo(a.admin, http.MethodGet, "/api/logs?affinity=any", nil, 200); out["total"].(float64) != 0 {
		t.Fatalf("logged while disabled: %v", out["total"])
	}
}

// gptEnv: gpt-6 served by two OpenAI channels behind round robin (a.up), and
// gpt-6-anth / claude-x served by an Anthropic channel (anth).
type gptEnv struct {
	*affinityEnv
	anth *recUpstream
}

func setupGPTAffinity(t *testing.T) *gptEnv {
	e := setupGateway(t)
	a := &affinityEnv{gwEnv: e, up: map[string]*recUpstream{}, names: map[string]string{}}
	for _, name := range []string{"gpt-a", "gpt-b"} {
		u := newRecUpstream(t)
		id := e.platformChannel(map[string]any{"name": name, "type": "openai", "baseUrl": u.srv.URL + "/v1", "models": models("gpt-6")})
		a.up[id], a.names[id] = u, name
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/routes", map[string]any{"name": "rr", "match": map[string]any{"models": []string{"gpt-6"}},
		"strategy": "round_robin"}, 201)
	g := &gptEnv{affinityEnv: a, anth: newRecUpstream(t)}
	e.channel(e.admin, map[string]any{"name": "anth", "type": "anthropic", "baseUrl": g.anth.srv.URL, "models": models("gpt-6-anth", "claude-x")})
	_, a.key = e.key(e.admin, map[string]any{"name": "k"})
	return g
}

// post sends a gateway request; it returns the status, the gpt-6 channel that
// received it ("" = none) and what that upstream (or the Anthropic one) got.
func (g *gptEnv) post(path, body string, headers ...string) (int, string, recRequest) {
	g.t.Helper()
	before := map[string]int{}
	for id, u := range g.up {
		before[id] = u.count()
	}
	anthBefore := g.anth.count()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, g.h.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+g.key)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		g.t.Fatalf("%s = %d %s", path, resp.StatusCode, raw)
	}
	for id, u := range g.up {
		if u.count() > before[id] {
			return resp.StatusCode, id, u.last()
		}
	}
	if g.anth.count() > anthBefore {
		return resp.StatusCode, "", g.anth.last()
	}
	g.t.Fatalf("%s reached no upstream", path)
	return 0, "", recRequest{}
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// upstreamIdentity checks the injected prompt_cache_key and Session_id.
func upstreamIdentity(t *testing.T, got recRequest) (string, string) {
	t.Helper()
	key, _ := got.body["prompt_cache_key"].(string)
	sid := got.header.Get("Session_id")
	if !strings.HasPrefix(key, "og-") || len(key) != 35 || !uuidRE.MatchString(sid) {
		t.Fatalf("upstream identity = %q / %q", key, sid)
	}
	return key, sid
}

func chatTurns(first string, turns int) string {
	msgs := []map[string]any{{"role": "system", "content": "You are a travel agent."}, {"role": "user", "content": first}}
	for i := 1; i < turns; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": fmt.Sprintf("answer %d", i)}, map[string]any{"role": "user", "content": fmt.Sprintf("follow-up %d", i)})
	}
	b, _ := json.Marshal(map[string]any{"model": "gpt-6", "messages": msgs})
	return string(b)
}

func anthropicTurns(model, userID string, turns int) string {
	msgs := []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": "Fix the failing test"}}}}
	for i := 1; i < turns; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": fmt.Sprintf("step %d", i)}, map[string]any{"role": "user", "content": fmt.Sprintf("continue %d", i)})
	}
	b, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 256, "metadata": map[string]any{"user_id": userID},
		"system": []map[string]any{{"type": "text", "text": "You are Claude Code."}}, "messages": msgs})
	return string(b)
}

// GPT models get session affinity on every inbound protocol ("gpt session"),
// and OpenAI-format upstream requests carry a stable per-conversation
// prompt_cache_key and Session_id.
func TestGPTSessionAffinity(t *testing.T) {
	g := setupGPTAffinity(t)

	// OpenAI Chat without any session identifier: the conversation anchor
	// keeps all turns on one channel with one upstream identity.
	_, rome, got := g.post("/v1/chat/completions", chatTurns("Plan a trip to Rome", 1))
	romeKey, romeSID := upstreamIdentity(t, got)
	for turns := 2; turns <= 4; turns++ {
		_, ch, got := g.post("/v1/chat/completions", chatTurns("Plan a trip to Rome", turns))
		if ch != rome {
			t.Fatalf("turn %d moved from %s to %s", turns, g.names[rome], g.names[ch])
		}
		if k, sid := upstreamIdentity(t, got); k != romeKey || sid != romeSID {
			t.Fatalf("turn %d identity changed: %s %s", turns, k, sid)
		}
		if len(got.body["messages"].([]any)) != 2*turns {
			t.Fatalf("messages lost: %v", got.body)
		}
	}
	// Another conversation gets another identity (and spreads by round robin).
	spread := map[string]string{"Rome": rome}
	for _, trip := range []string{"Paris", "Oslo", "Lima"} {
		_, ch, got := g.post("/v1/chat/completions", chatTurns("Plan a trip to "+trip, 1))
		spread[trip] = ch
		if k, sid := upstreamIdentity(t, got); k == romeKey || sid == romeSID {
			t.Fatalf("%s shares Rome's identity", trip)
		}
	}
	channels := map[string]bool{}
	for trip, ch := range spread {
		channels[ch] = true
		if _, again, _ := g.post("/v1/chat/completions", chatTurns("Plan a trip to "+trip, 2)); again != ch {
			t.Fatalf("%s moved", trip)
		}
	}
	if len(channels) != 2 {
		t.Fatal("conversations did not spread over both channels")
	}

	// The client's prompt_cache_key is forwarded unchanged (and is the session).
	_, pinned, got := g.post("/v1/chat/completions", `{"model":"gpt-6","prompt_cache_key":"client-pck","messages":[{"role":"user","content":"a"}]}`)
	if got.body["prompt_cache_key"] != "client-pck" || !uuidRE.MatchString(got.header.Get("Session_id")) {
		t.Fatalf("client prompt_cache_key: %v / %q", got.body["prompt_cache_key"], got.header.Get("Session_id"))
	}
	if _, ch, _ := g.post("/v1/chat/completions", `{"model":"gpt-6","prompt_cache_key":"client-pck","messages":[{"role":"user","content":"b"}]}`); ch != pinned {
		t.Fatal("client prompt_cache_key session moved")
	}

	// Claude Code calling a gpt model: Anthropic → Chat conversion; the
	// session comes from metadata.user_id.
	cc := "user_0123abcd_account__session_6f1c2d3e-0000-4000-8000-000000000001"
	_, ccCh, got := g.post("/v1/messages", anthropicTurns("gpt-6", cc, 1), "User-Agent", "claude-cli/2.1.0 (external, cli)")
	ccKey, ccSID := upstreamIdentity(t, got)
	if ccKey == romeKey || got.header.Get("User-Agent") != "claude-cli/2.1.0 (external, cli)" {
		t.Fatalf("claude code → gpt: %q, UA %q", ccKey, got.header.Get("User-Agent"))
	}
	for turns := 2; turns <= 3; turns++ {
		_, ch, got := g.post("/v1/messages", anthropicTurns("gpt-6", cc, turns))
		if k, sid := upstreamIdentity(t, got); ch != ccCh || k != ccKey || sid != ccSID {
			t.Fatalf("claude code turn %d: %s %s %s", turns, g.names[ch], k, sid)
		}
	}

	// Codex on /v1/responses with its own prompt_cache_key and Session_id:
	// both reach the upstream unchanged (Responses → Chat conversion).
	_, cx, got := g.post("/v1/responses", `{"model":"gpt-6","input":"hi","prompt_cache_key":"codex-thread-1"}`, "Session_id", "codex-thread-1",
		"Originator", "codex_cli_rs", "X-Codex-Turn-Metadata", `{"turn":1}`)
	if got.body["prompt_cache_key"] != "codex-thread-1" || got.header.Get("Session_id") != "codex-thread-1" || got.header.Get("Originator") != "codex_cli_rs" ||
		got.header.Get("X-Codex-Turn-Metadata") != `{"turn":1}` {
		t.Fatalf("codex: %v / %v", got.body["prompt_cache_key"], got.header)
	}
	if v := got.header.Values("Session_id"); len(v) != 1 {
		t.Fatalf("Session_id values = %v", v)
	}
	if _, ch, _ := g.post("/v1/responses", `{"model":"gpt-6","input":"again","prompt_cache_key":"codex-thread-1"}`, "Session_id", "codex-thread-1"); ch != cx {
		t.Fatal("codex session moved")
	}

	// Anthropic-format upstreams get nothing injected: a gpt model ("gpt
	// session" applies) and a Claude model ("claude cli trace").
	for _, model := range []string{"gpt-6-anth", "claude-x"} {
		_, ch, got := g.post("/v1/messages", anthropicTurns(model, cc, 1), "X-Claude-Code-Session-Id", "6f1c2d3e")
		if ch != "" {
			t.Fatalf("%s served by %s", model, g.names[ch])
		}
		if _, ok := got.body["prompt_cache_key"]; ok || got.header.Get("Session_id") != "" {
			t.Fatalf("%s: injected into an Anthropic upstream: %v / %v", model, got.body, got.header)
		}
		// "claude cli trace" passes Claude Code's headers ("gpt session" Codex's).
		if passed := got.header.Get("X-Claude-Code-Session-Id") == "6f1c2d3e"; passed != (model == "claude-x") {
			t.Fatalf("%s: X-Claude-Code-Session-Id passed = %v", model, passed)
		}
	}

	// Request logs name the rule; never the session values or derived ids.
	if err := g.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := g.mustDo(g.admin, http.MethodGet, "/api/logs?pageSize=200", nil, 200)
	rules := map[string]string{}
	for _, it := range out["items"].([]any) {
		l := it.(map[string]any)
		rule, _ := l["affinityRule"].(string)
		if prev, ok := rules[l["model"].(string)]; ok && prev != rule {
			t.Fatalf("%s logged with %q and %q", l["model"], prev, rule)
		}
		rules[l["model"].(string)] = rule
	}
	if rules["gpt-6"] != "gpt session" || rules["gpt-6-anth"] != "gpt session" || rules["claude-x"] != "claude cli trace" {
		t.Fatalf("logged rules = %v", rules)
	}
	var leaked int
	if err := g.pool.QueryRow(context.Background(), `SELECT count(*) FROM request_logs WHERE affinity_rule LIKE '%og-%' OR affinity_rule LIKE '%session_6f1c%'
		OR affinity_rule LIKE '%codex-thread%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("session value logged: %d %v", leaked, err)
	}
	stats := g.mustDo(g.admin, http.MethodGet, "/api/admin/affinity/stats", nil, 200)
	if stats["rules"].(map[string]any)["gpt session"].(float64) < 6 {
		t.Fatalf("stats = %v", stats)
	}
}
