package engine_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"omnigate/internal/plugin"
	"omnigate/internal/plugin/engine"
)

const src = `
import { definePlugin } from "@omnigate/plugin-sdk"
export default definePlugin({
  capabilities: {
    async "balance.get"(_input: unknown, ctx: any) {
      const res = await og.fetch(ctx.url + "/balance", { headers: { Authorization: "Bearer " + og.secret("apiKey") } })
      og.log.info("status", res.status, "key", og.secret("apiKey"))
      const j = res.json()
      return { currency: j.currency, total: j.total, available: true }
    },
    async blocked() {
      try { await og.fetch("https://evil.example.com/x"); return "fetched" } catch (e: any) { return "denied: " + e.message }
    },
    loop() { while (true) {} },
    hog() { const a: string[] = []; for (;;) a.push("x".repeat(4096)) },
    pending() { return new Promise(() => {}) },
    big() { return "x".repeat(400000) },
    fail() { throw new Error("upstream said no") },
    sandbox() {
      let req = "blocked"
      const mod = ["f", "s"].join("")
      try { require(mod); req = "loaded" } catch (e) {}
      return [req, typeof process, typeof setTimeout, typeof og.fetch, typeof console.log]
    },
    store() { const n = (og.storage.get("n") ?? 0) + 1; og.storage.set("n", n); return n },
    sign() { return og.crypto.hmacSha256(og.secret("apiKey"), "msg") },
  },
  transformRequest(req: any) { req.body.extra = true; req.path = "/v2" + req.path; return req },
  signRequest(req: any) { req.headers["X-Sig"] = "k=" + og.secret("apiKey"); return req },
})`

func program(t *testing.T, cfg engine.Config) (*engine.Engine, *engine.Program) {
	t.Helper()
	bundle, ds := plugin.Compile(map[string]string{"src/index.ts": src}, "src/index.ts")
	if bundle == "" {
		t.Fatalf("compile: %+v", ds)
	}
	e := engine.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p, err := e.Program("test", bundle)
	if err != nil {
		t.Fatal(err)
	}
	return e, p
}

type memStore struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func (s *memStore) Get(_ context.Context, k string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok, nil
}
func (s *memStore) Set(_ context.Context, k string, v json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}
func (s *memStore) Delete(_ context.Context, k string) error { return nil }

func kind(err error) string {
	if pe, ok := err.(*engine.Error); ok {
		return pe.Kind
	}
	return "?"
}

func TestCapabilityFetchAndSecrets(t *testing.T) {
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"currency":"CNY","total":"12.5"}`))
	}))
	defer srv.Close()
	_, p := program(t, engine.Config{})
	u, _ := url.Parse(srv.URL)
	env := &engine.Env{Network: []string{"$baseUrl"}, BaseHost: u.Hostname(), AllowHTTP: true, HTTP: srv.Client(),
		Secrets: map[string]string{"apiKey": "sk-very-secret-123"}, Context: map[string]any{"url": srv.URL}}
	res, err := p.Call(context.Background(), env, []string{"capabilities", "balance.get"}, time.Second, nil, env.Context)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Output) != `{"currency":"CNY","total":"12.5","available":true}` {
		t.Fatalf("output = %s", res.Output)
	}
	if gotAuth.Load() != "Bearer sk-very-secret-123" {
		t.Fatalf("upstream auth = %v", gotAuth.Load())
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "sk-very-secret") || strings.Contains(string(b), "og-secret") {
		t.Fatalf("secret leaked in result/logs: %s", b)
	}
	if len(res.Fetches) != 1 || res.Fetches[0].Status != 200 {
		t.Fatalf("fetches = %+v", res.Fetches)
	}

	res, _ = p.Call(context.Background(), env, []string{"capabilities", "blocked"}, time.Second)
	if !strings.Contains(string(res.Output), "denied") || !strings.Contains(string(res.Output), "permissions.network") {
		t.Fatalf("blocked = %s", res.Output)
	}
	res, err = p.Call(context.Background(), env, []string{"capabilities", "sign"}, time.Second)
	if err != nil || len(res.Output) != 66 { // quoted 64-char hex
		t.Fatalf("hmac = %s %v", res.Output, err)
	}
}

func TestLimitsAndErrors(t *testing.T) {
	var violations atomic.Int32
	e, p := program(t, engine.Config{HeapLimit: 64 << 20, OnViolation: func(string, string) { violations.Add(1) }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.RunWatchdog(ctx)
	env := &engine.Env{}

	start := time.Now()
	_, err := p.Call(context.Background(), env, []string{"capabilities", "loop"}, 100*time.Millisecond)
	if kind(err) != "timeout" || time.Since(start) > time.Second {
		t.Fatalf("loop: %v after %s", err, time.Since(start))
	}
	_, err = p.Call(context.Background(), env, []string{"capabilities", "hog"}, 20*time.Second)
	if kind(err) != "memory" {
		t.Fatalf("hog: %v", err)
	}
	if violations.Load() != 2 {
		t.Fatalf("violations = %d", violations.Load())
	}
	if _, err := p.Call(context.Background(), env, []string{"capabilities", "pending"}, time.Second); kind(err) != "promise" {
		t.Fatalf("pending: %v", err)
	}
	if _, err := p.Call(context.Background(), env, []string{"capabilities", "big"}, time.Second); kind(err) != "output" {
		t.Fatalf("big: %v", err)
	}
	if _, err := p.Call(context.Background(), env, []string{"capabilities", "fail"}, time.Second); kind(err) != "exception" || !strings.Contains(err.Error(), "upstream said no") {
		t.Fatalf("fail: %v", err)
	}
	if _, err := p.Call(context.Background(), env, []string{"capabilities", "nope"}, time.Second); kind(err) != "missing" {
		t.Fatalf("missing: %v", err)
	}
	res, err := p.Call(context.Background(), env, []string{"capabilities", "sandbox"}, time.Second)
	if err != nil || string(res.Output) != `["blocked","undefined","undefined","function","function"]` {
		t.Fatalf("sandbox globals = %s %v", res.Output, err)
	}
	// Cancellation interrupts promptly.
	cctx, ccancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, ccancel)
	if _, err := p.Call(cctx, env, []string{"capabilities", "loop"}, 5*time.Second); kind(err) != "cancelled" {
		t.Fatalf("cancel: %v", err)
	}
}

func TestHooksAndStorage(t *testing.T) {
	_, p := program(t, engine.Config{})
	env := &engine.Env{Secrets: map[string]string{"apiKey": "sk-hook"}, Storage: &memStore{m: map[string]json.RawMessage{}}}
	req := map[string]any{"dialect": "openai.chat", "path": "/chat/completions", "headers": map[string]string{}, "body": map[string]any{"model": "m"}}
	res, err := p.RunHooks(context.Background(), env, []string{"transformRequest", "signRequest"}, req, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Body    map[string]any    `json:"body"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Path != "/v2/chat/completions" || out.Headers["X-Sig"] != "k=sk-hook" || out.Body["extra"] != true {
		t.Fatalf("hooks = %s", res.Output)
	}
	for i := 1; i <= 2; i++ {
		res, err := p.Call(context.Background(), env, []string{"capabilities", "store"}, time.Second)
		if err != nil || string(res.Output) != string(rune('0'+i)) {
			t.Fatalf("store #%d = %s %v", i, res.Output, err)
		}
	}
	if _, err := p.Call(context.Background(), &engine.Env{}, []string{"capabilities", "store"}, time.Second); kind(err) != "exception" {
		t.Fatalf("storage without permission: %v", err)
	}
}

func TestMockFetchNeverTouchesNetwork(t *testing.T) {
	_, p := program(t, engine.Config{})
	var m engine.MockFetch
	m.Match.URL = "https://api.example.com/balance"
	m.Response.JSON = json.RawMessage(`{"currency":"USD","total":"1"}`)
	env := &engine.Env{Network: []string{"api.example.com"}, Mocks: []engine.MockFetch{m},
		Secrets: map[string]string{"apiKey": "k"}, Context: map[string]any{"url": "https://api.example.com"}}
	res, err := p.Call(context.Background(), env, []string{"capabilities", "balance.get"}, time.Second, nil, env.Context)
	if err != nil || !strings.Contains(string(res.Output), `"USD"`) {
		t.Fatalf("mock = %s %v", res.Output, err)
	}
	env.Context = map[string]any{"url": "https://api.example.com/other"}
	if _, err := p.Call(context.Background(), env, []string{"capabilities", "balance.get"}, time.Second, nil, env.Context); err == nil {
		t.Fatal("unmatched mock must fail")
	}
}

func BenchmarkHookCall(b *testing.B) {
	bundle, _ := plugin.Compile(map[string]string{"src/index.ts": src}, "src/index.ts")
	e := engine.New(engine.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p, _ := e.Program("bench", bundle)
	env := &engine.Env{Secrets: map[string]string{"apiKey": "k"}}
	req := map[string]any{"path": "/x", "headers": map[string]string{}, "body": map[string]any{"model": "m"}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.RunHooks(context.Background(), env, []string{"transformRequest", "signRequest"}, req, 50*time.Millisecond); err != nil {
			b.Fatal(err)
		}
	}
}

// Hooks return the whole request; large chat bodies must not hit the
// capability output limit.
func TestHooksAcceptLargeBodies(t *testing.T) {
	_, p := program(t, engine.Config{})
	env := &engine.Env{Secrets: map[string]string{"apiKey": "k"}}
	big := strings.Repeat("x", 1<<20)
	req := map[string]any{"path": "/chat/completions", "headers": map[string]string{}, "body": map[string]any{"messages": []any{map[string]any{"role": "user", "content": big}}}}
	res, err := p.RunHooks(context.Background(), env, []string{"transformRequest", "signRequest"}, req, 200*time.Millisecond)
	if err != nil || len(res.Output) < 1<<20 {
		t.Fatalf("large body through hooks: %v (output %d bytes)", err, len(res.Output))
	}
}

func TestHookBudgetScalesWithBodySize(t *testing.T) {
	base := 50 * time.Millisecond
	for n, want := range map[int]time.Duration{
		0:         base,
		1:         base + 20*time.Millisecond,
		64 << 10:  base + 20*time.Millisecond,
		1 << 20:   base + 320*time.Millisecond,
		32 << 20:  2 * time.Second, // capped
		200 << 20: 2 * time.Second,
	} {
		if got := engine.HookBudget(base, n); got != want {
			t.Errorf("HookBudget(%d) = %v, want %v", n, got, want)
		}
	}
}
