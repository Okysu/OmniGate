package app_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"omnigate/internal/app"
	"omnigate/internal/channel"
)

// A model served by one channel (ADR-0011): a short upstream restart is
// absorbed by retries on the same channel, a rate limit only pauses the model
// and the paused channel still serves as the last resort.
func TestBreakerSingleChannelResilience(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newModelUpstream(t, 0)
	chID := e.platformChannel(map[string]any{"name": "pool", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("m1", "m2")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	// Two 502s while the upstream restarts: the third attempt succeeds.
	up.failFirst.Store(2)
	start := time.Now()
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1"))); code != 200 {
		t.Fatalf("restart = %d %s", code, raw)
	}
	if d := time.Since(start); up.hits.Load() != 3 || d < 1500*time.Millisecond || d > 6*time.Second {
		t.Fatalf("hits = %d after %s", up.hits.Load(), d)
	}
	if l := e.lastLog(e.admin, "model=m1"); l["attempts"] != float64(3) || l["statusCode"] != float64(200) {
		t.Fatalf("log = %v", l)
	}
	if _, ch := e.admin.do(http.MethodGet, "/api/channels/"+chID, nil); ch["health"].(map[string]any)["state"] != "healthy" {
		t.Fatalf("a request that recovered counts as a success: %v", ch["health"])
	}

	// A 429 pauses m1 on the channel; m2 keeps working, and m1 is still
	// served through the last-resort attempt once the upstream answers again.
	up.status.Store(429)
	if code, _, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1"))); code != 429 {
		t.Fatalf("rate limited = %d", code)
	}
	_, ch := e.admin.do(http.MethodGet, "/api/channels/"+chID, nil)
	h := ch["health"].(map[string]any)
	ms := h["models"].([]any)
	if h["state"] != "degraded" || len(ms) != 1 || ms[0].(map[string]any)["model"] != "m1" || ms[0].(map[string]any)["state"] != "paused" {
		t.Fatalf("health = %v", h)
	}
	up.status.Store(0)
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m2"))); code != 200 {
		t.Fatalf("other model = %d %s", code, raw)
	}
	if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1"))); code != 200 {
		t.Fatalf("last resort = %d %s", code, raw)
	}
}

// When every circuit is open and no last-resort attempt may pass, the client
// gets 503 with Retry-After instead of a bare 502.
func TestBreakerOpenAnswers503(t *testing.T) {
	bc := channel.DefaultBreakerConfig()
	bc.NoMerge, bc.MinFailSeconds, bc.MaxForced = true, 1, 0
	bc.BaseCooldown, bc.MaxCooldown = 30*time.Second, 30*time.Second
	e := setupGatewayWith(t, app.Options{Breaker: &bc})
	base := e.h.srv.URL
	up := newModelUpstream(t, 500)
	e.platformChannel(map[string]any{"name": "pool", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	if code, _, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1"))); code != 502 {
		t.Fatalf("failing upstream = %d", code)
	}
	hits := up.hits.Load()
	resp := gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1"))
	ra := resp.Header.Get("Retry-After")
	code, body, raw := readBody(resp)
	if code != 503 || ra == "" || ra == "0" || errCode(body) != "upstream_unavailable" || up.hits.Load() != hits {
		t.Fatalf("open circuit = %d retry-after=%q %s (hits %d→%d)", code, ra, raw, hits, up.hits.Load())
	}
}

// An always-on channel keeps receiving requests while its circuit would be
// open.
func TestBreakerAlwaysOnChannel(t *testing.T) {
	bc := channel.DefaultBreakerConfig()
	bc.NoMerge, bc.MinFailSeconds, bc.MaxForced = true, 1, 0
	bc.BaseCooldown, bc.MaxCooldown = 30*time.Second, 30*time.Second
	e := setupGatewayWith(t, app.Options{Breaker: &bc})
	base := e.h.srv.URL
	up := newModelUpstream(t, 500)
	e.platformChannel(map[string]any{"name": "pool", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("m1"),
		"config": map[string]any{"alwaysOn": true}})
	_, key := e.key(e.admin, map[string]any{"name": "k"})
	for i := 0; i < 3; i++ {
		if code, _, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat("m1"))); code != 502 {
			t.Fatalf("request %d = %d, want the upstream's 502", i, code)
		}
	}
	// Every request reached the upstream (each with its same-channel retries).
	if up.hits.Load() < 3*3 {
		t.Fatalf("hits = %d", up.hits.Load())
	}
}

// Reasoning the upstream cannot verify is stripped and the request retried
// once on the same channel; the channel's health is untouched.
func TestReasoningVerificationRetry(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	var hits atomic.Int64
	var lastBody atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		lastBody.Store(string(b))
		if strings.Contains(string(b), `"type":"reasoning"`) {
			http.Error(w, `{"error":{"message":"The encrypted content for item rs_1 could not be verified. Reason: reasoning hydration failed: Encrypted content item_id did not match the target item id."}}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"up-m1","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	t.Cleanup(up.Close)
	chID := e.platformChannel(map[string]any{"name": "pool", "type": "openai", "baseUrl": up.URL + "/v1", "models": models("m1"),
		"config": map[string]any{"supportsResponses": true}})
	_, key := e.key(e.admin, map[string]any{"name": "k"})
	body := `{"model":"m1","input":[{"role":"user","content":"hi"},{"type":"reasoning","summary":[],"encrypted_content":"gAAAA-other-account"},{"role":"assistant","content":"x"},{"role":"user","content":"again"}]}`
	code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/responses", key, body))
	if code != 200 || hits.Load() != 2 || strings.Contains(lastBody.Load().(string), "reasoning") {
		t.Fatalf("retry = %d %s (hits %d)", code, raw, hits.Load())
	}
	if _, ch := e.admin.do(http.MethodGet, "/api/channels/"+chID, nil); ch["health"].(map[string]any)["state"] != "healthy" {
		t.Fatalf("health = %v", ch["health"])
	}
}
