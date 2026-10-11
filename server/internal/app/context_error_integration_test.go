package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A context-window error is the client's input: whatever status the upstream
// (or a proxy such as AxonHub with a Responses WebSocket transport, which
// answers a stream request with 200 and a JSON error body) used, the client
// gets 400 context_length_exceeded, no other channel is tried and the channel
// stays healthy however often it happens.
func TestContextLengthErrorIsClientError(t *testing.T) {
	e := setupGateway(t)
	ctx := context.Background()
	body := `{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"","param":"input"}}`
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if strings.Contains(r.URL.RawQuery, "nonstream") {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(up.Close)
	other := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "pool", "type": "openai", "scope": "global", "baseUrl": up.URL + "/v1", "priority": 10, "models": models("big")})
	e.platformChannel(map[string]any{"name": "backup", "type": "openai", "scope": "global", "baseUrl": other.srv.URL + "/v1", "priority": 1, "models": models("big")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	for i := 0; i < 8; i++ {
		code, out, raw := readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key,
			`{"model":"big","stream":true,"messages":[{"role":"user","content":"x"}]}`))
		errObj, _ := out["error"].(map[string]any)
		if code != 400 || errObj["code"] != "context_length_exceeded" || errObj["type"] != "invalid_request_error" {
			t.Fatalf("stream request %d = %d %s", i, code, raw)
		}
	}
	if other.hits.Load() != 0 {
		t.Fatalf("a context error must not be retried on another channel (backup hits %d)", other.hits.Load())
	}
	if hits.Load() != 8 {
		t.Fatalf("the pool channel must stay in rotation: %d hits", hits.Load())
	}
	chans := e.mustDo(e.admin, http.MethodGet, "/api/channels", nil, 200)["items"].([]any)
	for _, c := range chans {
		ch := c.(map[string]any)
		if h := ch["health"].(map[string]any); h["state"] != "healthy" {
			t.Fatalf("channel %s health = %v", ch["name"], h)
		}
	}
	// Anthropic clients get their own error shape.
	code, out, raw := readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/messages", key,
		`{"model":"big","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`))
	if code != 400 || out["error"].(map[string]any)["type"] != "invalid_request_error" {
		t.Fatalf("anthropic = %d %s", code, raw)
	}
}
