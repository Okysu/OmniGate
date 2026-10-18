package app_test

import (
	"context"
	"net/http"
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
