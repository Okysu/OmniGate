package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"omnigate/internal/app"
	"omnigate/internal/config"
	"omnigate/internal/platform/db"
)

// fakeUpstream is an OpenAI + Anthropic compatible upstream whose behaviour
// each test controls through mode.
type fakeUpstream struct {
	srv     *httptest.Server
	mode    atomic.Value // string
	hits    atomic.Int64
	release chan struct{}
	ctxDone chan struct{}
	lastKey atomic.Value
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	f := &fakeUpstream{release: make(chan struct{}), ctxDone: make(chan struct{}, 1)}
	f.mode.Store("ok")
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUpstream) setMode(m string) { f.mode.Store(m) }

func sse(w http.ResponseWriter, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}

func (f *fakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	f.lastKey.Store(r.Header.Get("Authorization") + r.Header.Get("x-api-key"))
	mode := f.mode.Load().(string)
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	stream, _ := req["stream"].(bool)
	switch mode {
	case "500":
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
		return
	case "slow-headers":
		time.Sleep(3 * time.Second)
	case "401":
		http.Error(w, `{"error":{"message":"Incorrect API key provided: sk-live-abcdefghijklmnopqrstuvwxyz"}}`, http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/v1/messages" {
		f.anthropic(w, r, mode, stream)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/embeddings") {
		if _, ok := req["stream"]; ok {
			http.Error(w, `{"error":{"message":"stream is not allowed for embeddings"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"up","usage":{"prompt_tokens":4,"total_tokens":4}}`)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/responses") {
		f.lastKey.Store(fmt.Sprint(req["input"]))
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":1,"model":"up","status":"completed","output":[{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"from responses"}]}],"usage":{"input_tokens":5,"output_tokens":2}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
			`{"type":"response.output_text.delta","item_id":"m","delta":"from "}`,
			`{"type":"response.output_text.delta","item_id":"m","delta":"responses"}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":5,"output_tokens":2}}}`,
		} {
			var m map[string]any
			_ = json.Unmarshal([]byte(e), &m)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m["type"], e)
			w.(http.Flusher).Flush()
		}
		return
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	sse(w, `{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"first"}}]}`)
	switch mode {
	case "block":
		select {
		case <-f.release:
		case <-r.Context().Done():
			f.ctxDone <- struct{}{}
			return
		}
	case "cut":
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
		return
	case "malformed":
		sse(w, `{not json`)
	}
	sse(w, `{"id":"c1","choices":[{"index":0,"delta":{"content":" second"},"finish_reason":"stop"}]}`)
	sse(w, `{"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	sse(w, `[DONE]`)
}

func (f *fakeUpstream) anthropic(w http.ResponseWriter, _ *http.Request, _ string, stream bool) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[{"type":"text","text":"hi from claude"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range []string{
		`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":7,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`{"type":"message_stop"}`,
	} {
		var m map[string]any
		_ = json.Unmarshal([]byte(e), &m)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m["type"], e)
		w.(http.Flusher).Flush()
	}
}

type gwEnv struct {
	t     *testing.T
	h     *harness
	app   *app.App
	cfg   *config.Config // to start more App instances on the same database
	pool  *db.DB
	admin *client
	carol *client
	ops   *client // bob, a channel_admin (created by platformChannel)
}

func setupGateway(t *testing.T) *gwEnv { return setupGatewayWith(t, app.Options{}) }

func setupGatewayWith(t *testing.T, opts app.Options) *gwEnv {
	dsn := testDB(t)
	gh := fakeGitHub(t)
	ctx := context.Background()
	env := map[string]string{
		"OMNIGATE_DATABASE_URL": dsn, "OMNIGATE_DATA_DIR": t.TempDir(), "OMNIGATE_PUBLIC_URL": "http://localhost:8080", "OMNIGATE_COOKIE_SECURE": "false",
		"OMNIGATE_REGISTRATION_MODE": "open", "OMNIGATE_BOOTSTRAP_ADMINS": "github:alice",
		"OMNIGATE_AUTH_GITHUB_CLIENT_ID": "gh-client", "OMNIGATE_AUTH_GITHUB_CLIENT_SECRET": "gh-secret",
		"OMNIGATE_AUTH_GITHUB_AUTH_URL": gh.URL + "/login/oauth/authorize", "OMNIGATE_AUTH_GITHUB_TOKEN_URL": gh.URL + "/login/oauth/access_token",
		"OMNIGATE_AUTH_GITHUB_API_URL": gh.URL, "OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK": "true",
	}
	cfg, err := config.LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, cfg, log, pool, opts)
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	a.Start(wctx)
	t.Cleanup(func() { cancel(); a.Stop() })
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	e := &gwEnv{t: t, h: &harness{t: t, srv: srv}, app: a, cfg: cfg, pool: pool}
	e.admin, e.carol = e.h.newClient(), e.h.newClient()
	e.admin.login("alice")
	e.carol.login("carol")
	return e
}

// channel creates a channel through the API and waits for the registry reload.
func (e *gwEnv) channel(c *client, body map[string]any) string {
	e.t.Helper()
	if body["apiKey"] == nil {
		body["apiKey"] = "sk-upstream-secret-0123456789"
	}
	resp, out := c.do(http.MethodPost, "/api/channels", body)
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("create channel = %d %v", resp.StatusCode, out)
	}
	time.Sleep(50 * time.Millisecond) // registry reloads asynchronously
	return out["id"].(string)
}

// userID returns the id of the user signed in on c.
func (e *gwEnv) userID(c *client) string {
	e.t.Helper()
	_, me := c.do(http.MethodGet, "/api/me", nil)
	return me["user"].(map[string]any)["id"].(string)
}

// platformChannel creates a channel owned by a second administrator (bob,
// channel_admin), so it is a platform-tier channel for alice: billed and
// subject to route rules (phase5-api.md §1). Without an explicit scope it is
// shared with alice only (for everyone else it is as private as alice's own).
func (e *gwEnv) platformChannel(body map[string]any) string {
	e.t.Helper()
	if e.ops == nil {
		e.ops = e.h.newClient()
		e.ops.login("bob")
		bobID := e.userID(e.ops)
		users := e.mustDo(e.admin, http.MethodGet, "/api/admin/users?q=bob", nil, 200)
		var version any
		for _, it := range users["items"].([]any) {
			if u := it.(map[string]any); u["id"] == bobID {
				version = u["version"]
			}
		}
		e.mustDo(e.admin, http.MethodPatch, "/api/admin/users/"+bobID, map[string]any{"role": "channel_admin", "version": version}, 200)
	}
	if body["scope"] == nil {
		body["scope"], body["sharedWith"] = "shared", []string{e.userID(e.admin)}
		id := e.channel(e.ops, body)
		e.acceptShare(e.admin, id) // user shares need acceptance (phase5-api.md §5)
		return id
	}
	return e.channel(e.ops, body)
}

func (e *gwEnv) key(c *client, body map[string]any) (id, secret string) {
	e.t.Helper()
	resp, out := c.do(http.MethodPost, "/api/keys", body)
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("create key = %d %v", resp.StatusCode, out)
	}
	return out["key"].(map[string]any)["id"].(string), out["secret"].(string)
}

func gwPost(t *testing.T, ctx context.Context, base, path, key, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(r *http.Response) (int, map[string]any, string) {
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return r.StatusCode, m, string(b)
}

const chatBody = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
const chatStream = `{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func models(names ...string) []map[string]string {
	var out []map[string]string
	for _, n := range names {
		out = append(out, map[string]string{"model": n, "upstreamModel": "up-" + n})
	}
	return out
}

func TestGatewayStreamingAndFallback(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newFakeUpstream(t)
	bad := newFakeUpstream(t)
	bad.setMode("500")
	badID := e.channel(e.admin, map[string]any{"name": "bad", "type": "openai", "baseUrl": bad.srv.URL + "/v1", "priority": 10, "models": models("m1")})
	e.channel(e.admin, map[string]any{"name": "good", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	t.Run("fallback on 5xx then circuit opens", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			code, body, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chatBody))
			if code != 200 || body["choices"] == nil {
				t.Fatalf("attempt %d = %d %s", i, code, raw)
			}
		}
		if bad.hits.Load() != 3 {
			t.Fatalf("bad channel hits = %d", bad.hits.Load())
		}
		readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chatBody))
		if bad.hits.Load() != 3 {
			t.Fatal("open circuit still received traffic")
		}
		_, ch := e.admin.do(http.MethodGet, "/api/channels/"+badID, nil)
		if ch["health"].(map[string]any)["state"] != "open" {
			t.Fatalf("health = %v", ch["health"])
		}
	})

	t.Run("first byte is streamed before upstream finishes", func(t *testing.T) {
		up.setMode("block")
		resp := gwPost(t, context.Background(), base, "/v1/chat/completions", key, chatStream)
		defer resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("status %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		rd := bufio.NewReader(resp.Body)
		got := make(chan string, 1)
		go func() { line, _ := rd.ReadString('\n'); got <- line }()
		select {
		case line := <-got:
			if !strings.Contains(line, "first") {
				t.Fatalf("first line = %q", line)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("first chunk was buffered until upstream completion")
		}
		up.release <- struct{}{}
		rest, _ := io.ReadAll(rd)
		if !strings.Contains(string(rest), "[DONE]") || strings.Contains(string(rest), "usage") {
			t.Fatalf("rest = %q", rest)
		}
	})

	t.Run("client cancel propagates to upstream", func(t *testing.T) {
		up.setMode("block")
		ctx, cancel := context.WithCancel(context.Background())
		resp := gwPost(t, ctx, base, "/v1/chat/completions", key, chatStream)
		buf := make([]byte, 64)
		_, _ = resp.Body.Read(buf)
		cancel()
		resp.Body.Close()
		select {
		case <-up.ctxDone:
		case <-time.After(3 * time.Second):
			t.Fatal("upstream request was not cancelled")
		}
	})

	t.Run("mid-stream failure does not switch channels", func(t *testing.T) {
		up.setMode("cut")
		before := up.hits.Load()
		_, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chatStream))
		if !strings.Contains(raw, "first") || !strings.Contains(raw, "upstream_invalid_response") {
			t.Fatalf("stream = %q", raw)
		}
		if up.hits.Load() != before+1 {
			t.Fatal("request was retried after bytes were written")
		}
	})

	t.Run("malformed SSE is reported in-band when converting", func(t *testing.T) {
		up.setMode("malformed")
		body := `{"model":"m1","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		_, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/messages", key, body))
		if !strings.Contains(raw, "event: error") || !strings.Contains(raw, "first") {
			t.Fatalf("stream = %q", raw)
		}
	})

	t.Run("upstream secrets never reach clients or logs", func(t *testing.T) {
		up.setMode("401")
		e.app.FlushLogs(context.Background())
		_, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chatBody))
		if strings.Contains(raw, "sk-live") || strings.Contains(raw, "sk-upstream") {
			t.Fatalf("secret leaked to client: %s", raw)
		}
		if k := up.lastKey.Load().(string); k != "Bearer sk-upstream-secret-0123456789" {
			t.Fatalf("upstream got auth %q", k)
		}
		if err := e.app.FlushLogs(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, logs := e.admin.do(http.MethodGet, "/api/logs?pageSize=200", nil)
		b, _ := json.Marshal(logs)
		if strings.Contains(string(b), "sk-live") || strings.Contains(string(b), "sk-upstream") || strings.Contains(string(b), key) {
			t.Fatal("secret found in request logs")
		}
	})

	t.Run("request logs record fallback path and usage", func(t *testing.T) {
		if err := e.app.FlushLogs(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, logs := e.admin.do(http.MethodGet, "/api/logs?pageSize=200", nil)
		var withFallback map[string]any
		for _, it := range logs["items"].([]any) {
			m := it.(map[string]any)
			if m["attempts"].(float64) == 2 {
				withFallback = m
			}
		}
		if withFallback == nil {
			t.Fatal("no log entry with 2 attempts")
		}
		path := withFallback["fallbackPath"].([]any)
		if path[0].(map[string]any)["statusCode"].(float64) != 500 || withFallback["statusCode"].(float64) != 200 {
			t.Fatalf("fallback entry = %v", withFallback)
		}
		if withFallback["usage"].(map[string]any)["input"].(float64) != 10 {
			t.Fatalf("usage = %v", withFallback["usage"])
		}
	})
}

func TestGatewayHeaderTimeoutFallback(t *testing.T) {
	e := setupGateway(t)
	slow, fast := newFakeUpstream(t), newFakeUpstream(t)
	slow.setMode("slow-headers")
	e.channel(e.admin, map[string]any{"name": "slow", "type": "openai", "baseUrl": slow.srv.URL + "/v1", "priority": 5,
		"config": map[string]any{"timeoutSeconds": 1}, "models": models("m1")})
	e.channel(e.admin, map[string]any{"name": "fast", "type": "openai", "baseUrl": fast.srv.URL + "/v1", "models": models("m1")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})
	start := time.Now()
	code, _, raw := readBody(gwPost(t, context.Background(), e.h.srv.URL, "/v1/chat/completions", key, chatBody))
	if code != 200 || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("code=%d after %s: %s", code, time.Since(start), raw)
	}
}

func TestGatewayAccessControl(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newFakeUpstream(t)
	privID := e.channel(e.admin, map[string]any{"name": "alice-private", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("priv")})
	globalID := e.channel(e.admin, map[string]any{"name": "global", "type": "anthropic", "baseUrl": up.srv.URL, "scope": "global", "models": models("glob")})
	_, carolKey := e.key(e.carol, map[string]any{"name": "ck"})

	if resp, _ := e.carol.do(http.MethodGet, "/api/channels/"+privID, nil); resp.StatusCode != 404 {
		t.Fatalf("carol sees private channel: %d", resp.StatusCode)
	}
	_, g := e.carol.do(http.MethodGet, "/api/channels/"+globalID, nil)
	if g["baseUrl"] != nil || g["secret"] != nil || g["name"] != "global" {
		t.Fatalf("global channel leaks config to users: %v", g)
	}
	if resp, _ := e.carol.do(http.MethodPatch, "/api/channels/"+globalID, map[string]any{"name": "x", "version": 1}); resp.StatusCode != 403 {
		t.Fatalf("carol edited global channel: %d", resp.StatusCode)
	}
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", carolKey, `{"model":"priv","messages":[{"role":"user","content":"x"}]}`)); code != 404 {
		t.Fatalf("carol used alice's private channel: %d %s", code, raw)
	}
	// OpenAI client -> Anthropic global channel (cross-protocol, non-stream).
	code, body, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", carolKey, `{"model":"glob","messages":[{"role":"user","content":"x"}]}`))
	if code != 200 || body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] != "hi from claude" {
		t.Fatalf("cross-protocol via global channel = %d %s", code, raw)
	}

	// count_tokens: the fake upstream has no count_tokens endpoint, so the gateway falls back to an estimate.
	code, body, raw = readBody(gwPost(t, context.Background(), base, "/v1/messages/count_tokens", carolKey, `{"model":"glob","messages":[{"role":"user","content":"hello world"}]}`))
	if code != 200 || body["input_tokens"] == nil || body["choices"] != nil {
		t.Fatalf("count_tokens = %d %s", code, raw)
	}

	// Sharing grants use but not management.
	_, ch := e.admin.do(http.MethodGet, "/api/channels/"+privID, nil)
	_, me := e.carol.do(http.MethodGet, "/api/me", nil)
	carolID := me["user"].(map[string]any)["id"].(string)
	resp, out := e.admin.do(http.MethodPatch, "/api/channels/"+privID, map[string]any{"scope": "shared", "sharedWith": []string{carolID}, "version": ch["version"]})
	if resp.StatusCode != 200 {
		t.Fatalf("share = %d %v", resp.StatusCode, out)
	}
	time.Sleep(50 * time.Millisecond)
	// Not usable until carol accepts (phase5-api.md §5).
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", carolKey, `{"model":"priv","messages":[{"role":"user","content":"x"}]}`)); code != 404 {
		t.Fatalf("pending share usable: %d %s", code, raw)
	}
	e.acceptShare(e.carol, privID)
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", carolKey, `{"model":"priv","messages":[{"role":"user","content":"x"}]}`)); code != 200 {
		t.Fatalf("shared channel unusable: %d %s", code, raw)
	}
	_, shared := e.carol.do(http.MethodGet, "/api/channels/"+privID, nil)
	if shared["baseUrl"] != nil || shared["sharedWith"] != nil {
		t.Fatalf("shared channel leaks config: %v", shared)
	}

	// A plain user cannot point a channel at internal addresses.
	resp, out = e.carol.do(http.MethodPost, "/api/channels", map[string]any{"name": "ssrf", "type": "openai",
		"baseUrl": "http://169.254.169.254/latest", "apiKey": "x", "models": models("m")})
	if resp.StatusCode != 422 || out["error"].(map[string]any)["code"] != "base_url_not_allowed" {
		t.Fatalf("ssrf channel = %d %v", resp.StatusCode, out)
	}

	// Key policies.
	_, restricted := e.key(e.carol, map[string]any{"name": "r", "policy": map[string]any{"allowedModels": []string{"priv"}, "ipAllowlist": []string{"10.0.0.0/8"}}})
	if code, body, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", restricted, `{"model":"priv","messages":[{"role":"user","content":"x"}]}`)); code != 403 || !strings.Contains(fmt.Sprint(body), "IP") {
		t.Fatalf("ip allowlist = %d %v", code, body)
	}
	kid, k2 := e.key(e.carol, map[string]any{"name": "models", "policy": map[string]any{"allowedModels": []string{"priv"}}})
	if code, _, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", k2, `{"model":"glob","messages":[{"role":"user","content":"x"}]}`)); code != 403 {
		t.Fatalf("model allowlist = %d", code)
	}
	if resp, _ := e.carol.do(http.MethodDelete, "/api/keys/"+kid, nil); resp.StatusCode != 204 {
		t.Fatal("revoke failed")
	}
	if code, _, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", k2, chatBody)); code != 401 {
		t.Fatalf("revoked key = %d", code)
	}

	// Disabling a user stops their keys immediately (cache is flushed).
	if code, _, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", carolKey, `{"model":"glob","messages":[{"role":"user","content":"x"}]}`)); code != 200 {
		t.Fatal("precondition: carol's key works")
	}
	_, u := e.admin.do(http.MethodGet, "/api/admin/users?q=carol", nil)
	cu := u["items"].([]any)[0].(map[string]any)
	if resp, _ := e.admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"status": "disabled", "disabledReason": "test", "version": cu["version"]}); resp.StatusCode != 200 {
		t.Fatal("disable failed")
	}
	// phase7-api.md §2.1: 403 account_disabled (was 401 before Round 6 continued).
	if code, body, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", carolKey, `{"model":"glob","messages":[{"role":"user","content":"x"}]}`)); code != 403 || body["error"].(map[string]any)["code"] != "account_disabled" {
		t.Fatalf("disabled user's key = %d %v", code, body)
	}
}

func TestGatewayResponsesConversions(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newFakeUpstream(t)
	e.channel(e.admin, map[string]any{"name": "oa", "type": "openai", "baseUrl": up.srv.URL + "/v1",
		"models": []map[string]string{{"model": "gpt-r", "upstreamModel": "up-r", "upstreamProtocol": "responses"}, {"model": "chat-only", "upstreamModel": "up-c"}}})
	e.channel(e.admin, map[string]any{"name": "an", "type": "anthropic", "baseUrl": up.srv.URL, "models": models("claude-x")})
	_, key := e.key(e.admin, map[string]any{"name": "k", "policy": map[string]any{"compatMode": "lenient"}})
	e.mustDo(e.admin, http.MethodPost, "/api/channels", map[string]any{"name": "bad", "type": "anthropic", "baseUrl": up.srv.URL, "apiKey": "sk-x-0123456789",
		"models": []map[string]string{{"model": "m", "upstreamProtocol": "responses"}}}, 422)

	// Chat client -> Responses-only model.
	code, body, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, `{"model":"gpt-r","messages":[{"role":"user","content":"hi"}]}`))
	if code != 200 || body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] != "from responses" {
		t.Fatalf("chat->responses = %d %s", code, raw)
	}
	// Anthropic client (streaming) -> Responses-only model.
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(`{"model":"gpt-r","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _, raw = readBody(resp)
	if !strings.Contains(raw, "event: message_start") || !strings.Contains(raw, `"text":"from "`) || !strings.Contains(raw, "event: message_stop") {
		t.Fatalf("anthropic->responses stream = %s", raw)
	}
	// Embeddings: OpenAI channels only.
	code, body, raw = readBody(gwPost(t, context.Background(), base, "/v1/embeddings", key, `{"model":"chat-only","input":"hello","stream":true}`))
	if code != 200 || body["data"] == nil {
		t.Fatalf("embeddings = %d %s", code, raw)
	}
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/embeddings", key, `{"model":"claude-x","input":"hello"}`)); code != 404 || !strings.Contains(raw, "OpenAI-compatible") {
		t.Fatalf("embeddings on anthropic channel = %d %s", code, raw)
	}
	// Responses client -> Chat-only model (conversion, no supportsResponses needed).
	code, body, raw = readBody(gwPost(t, context.Background(), base, "/v1/responses", key, `{"model":"chat-only","input":"hi"}`))
	if code != 200 || body["object"] != "response" || !strings.Contains(raw, `"output_text"`) || !strings.Contains(raw, "hello") {
		t.Fatalf("responses->chat = %d %s", code, raw)
	}
	// Responses client (streaming) -> Anthropic channel.
	_, _, raw = readBody(gwPost(t, context.Background(), base, "/v1/responses", key, `{"model":"claude-x","input":"hi","stream":true}`))
	if !strings.Contains(raw, "event: response.created") || !strings.Contains(raw, "response.output_text.delta") || !strings.Contains(raw, "event: response.completed") {
		t.Fatalf("responses->anthropic stream = %s", raw)
	}
	if err := e.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, logs := e.admin.do(http.MethodGet, "/api/logs?pageSize=20&status=success", nil)
	if n := len(logs["items"].([]any)); n != 5 {
		t.Fatalf("successful logs = %d", n)
	}
	for _, it := range logs["items"].([]any) {
		if m := it.(map[string]any); m["usage"].(map[string]any)["input"].(float64) == 0 {
			t.Errorf("log without usage = %v", m)
		}
	}
}

func TestGatewayPlanQuotas(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "oa", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("m1", "m2")})
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m1", "inputPerM": "1", "outputPerM": "2"}, 201)
	plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
		"name": "测试套餐", "description": "", "duration": "30d", "models": []string{"m1"}, "stackable": false,
		"rules": []map[string]any{{"id": "5h", "label": "5 小时窗口", "meter": "requests", "window": map[string]any{"kind": "session", "duration": "5h"}, "limit": "2"}},
	}, 201)
	_, me := e.admin.do(http.MethodGet, "/api/me", nil)
	adminID := me["user"].(map[string]any)["id"].(string)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": adminID, "planId": plan["id"], "periods": 1}, 201)
	_, key := e.key(e.admin, map[string]any{"name": "k"})
	call := func(model string) *http.Response {
		return gwPost(t, context.Background(), base, "/v1/chat/completions", key, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model))
	}
	for i := 0; i < 2; i++ {
		if code, _, raw := readBody(call("m1")); code != 200 {
			t.Fatalf("covered request %d = %d %s", i, code, raw)
		}
		e.app.FlushLogs(context.Background()) // Record runs asynchronously
	}
	resp := call("m1")
	code, body, raw := readBody(resp)
	if code != 429 || body["error"].(map[string]any)["code"] != "quota_exceeded" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("over quota = %d retry-after=%q %s", code, resp.Header.Get("Retry-After"), raw)
	}
	if ra, _ := strconv.Atoi(resp.Header.Get("Retry-After")); ra < 4*3600 || ra > 5*3600 {
		t.Fatalf("retry-after = %d", ra)
	}
	if code, _, raw := readBody(call("m2")); code != 200 {
		t.Fatalf("uncovered model should use wallet path: %d %s", code, raw)
	}
	// The user decides what happens when quota is used up: pay from the wallet…
	e.mustDo(e.admin, http.MethodPut, "/api/billing/preferences", map[string]any{"quotaOverflow": "wallet"}, 200)
	if p := e.mustDo(e.admin, http.MethodGet, "/api/billing/preferences", nil, 200); p["quotaOverflow"] != "wallet" {
		t.Fatalf("preferences = %v", p)
	}
	if code, _, raw := readBody(call("m1")); code != 200 {
		t.Fatalf("overflow to wallet = %d %s", code, raw)
	}
	// …unless this key says block.
	_, blockKey := e.key(e.admin, map[string]any{"name": "kb", "policy": map[string]any{"quotaOverflow": "block"}})
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", blockKey, `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)); code != 429 {
		t.Fatalf("key block override = %d %s", code, raw)
	}
	e.mustDo(e.admin, http.MethodPut, "/api/billing/preferences", map[string]any{"quotaOverflow": "block"}, 200)
	if err := e.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, logs := e.admin.do(http.MethodGet, "/api/logs?pageSize=10&model=m1&status=success", nil)
	covered := 0
	for _, it := range logs["items"].([]any) {
		m := it.(map[string]any)
		if m["subscriptionId"] == nil {
			if m["charge"] == "0" { // the overflow request is billed to the wallet
				t.Fatalf("overflow log = %v", m)
			}
			continue
		}
		covered++
		if m["charge"] != "0" || m["quotaCharge"] == "0" {
			t.Fatalf("covered log = %v", m)
		}
	}
	if covered != 2 {
		t.Fatalf("covered logs = %d", covered)
	}
	subs := e.mustDo(e.admin, http.MethodGet, "/api/billing/subscriptions", nil, 200)
	rule := subs["items"].([]any)[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if rule["used"] != "2" || rule["exceeded"] != true || rule["resetsAt"] == nil {
		t.Fatalf("subscription usage = %v", rule)
	}
	// Redeeming a plan code renews the (non-stackable) subscription.
	batch := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/redeem-batches", map[string]any{"kind": "plan", "planId": plan["id"], "periods": 1, "count": 1}, 201)
	r := e.mustDo(e.admin, http.MethodPost, "/api/billing/redeem", map[string]any{"code": batch["codes"].([]any)[0]}, 200)
	if r["kind"] != "plan" || r["subscription"] == nil {
		t.Fatalf("redeem plan = %v", r)
	}
	subs = e.mustDo(e.admin, http.MethodGet, "/api/billing/subscriptions", nil, 200)
	if n := len(subs["items"].([]any)); n != 1 {
		t.Fatalf("renewal should extend, got %d subscriptions", n)
	}
}

// TestGatewayModelMissingFallback: an upstream that reports an unknown model
// with HTTP 400 is channel-specific, so the next channel is tried; a genuine
// 400 (bad request body) is returned to the client without retrying.
// Upstream 4xx other than 401/403/404/408/429 are classified by status code
// only (never by message): not retried by default, retried on another channel
// once the client_error class is enabled in gateway.retryOn.
func TestGatewayClientErrorRetry(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	var badMsg atomic.Value
	badMsg.Store(`{"error":{"message":"model not found: m1"}}`)
	var badHits atomic.Int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, badMsg.Load().(string))
	}))
	t.Cleanup(bad.Close)
	good := newFakeUpstream(t)
	badID := e.channel(e.admin, map[string]any{"name": "relay-a", "type": "openai", "baseUrl": bad.URL + "/v1", "priority": 10, "models": models("m1")})
	goodID := e.channel(e.admin, map[string]any{"name": "relay-b", "type": "openai", "baseUrl": good.srv.URL + "/v1", "models": models("m1")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	latestLog := func() map[string]any {
		t.Helper()
		if err := e.app.FlushLogs(context.Background()); err != nil {
			t.Fatal(err)
		}
		logs := e.mustDo(e.admin, http.MethodGet, "/api/logs?pageSize=1", nil, 200)
		return logs["items"].([]any)[0].(map[string]any)
	}

	// Default: a 400 is returned as is, whatever the message says.
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chatBody)); code != 400 {
		t.Fatalf("default 400 = %d %s", code, raw)
	}
	if badHits.Load() != 1 || good.hits.Load() != 0 {
		t.Fatalf("default 400 retried: bad %d good %d", badHits.Load(), good.hits.Load())
	}

	st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"gateway": map[string]any{"retryOn": []string{"rate_limit", "server_error", "timeout", "network", "auth_error", "not_found", "client_error"}}}}, 200)
	for _, msg := range []string{`{"error":{"message":"model not found: m1"}}`, `{"error":{"message":"messages: field required"}}`} {
		badMsg.Store(msg)
		if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chatBody)); code != 200 {
			t.Fatalf("client_error retry (%s) = %d %s", msg, code, raw)
		}
		l := latestLog()
		if l["attempts"].(float64) != 2 || l["channelId"] != goodID || l["statusCode"].(float64) != 200 {
			t.Fatalf("retry log = %v", l)
		}
		if path := l["fallbackPath"].([]any); path[0].(map[string]any)["channelId"] != badID || path[0].(map[string]any)["statusCode"].(float64) != 400 {
			t.Fatalf("fallback path = %v", path)
		}
	}
}
