package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// docs/contracts/phase14-api.md: legacy text completions (/v1/completions,
// FIM via suffix) are passthrough-only and served by openai channels that
// declare supportsCompletions.

type fimRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

// fimUpstream is a DeepSeek-style beta API: /completions answers with
// text_completion objects (streamed when asked, with a usage chunk only when
// stream_options.include_usage is set) and DeepSeek's prompt cache counters
// (1000 of 1200 prompt tokens hit); any other path answers Chat Completions.
type fimUpstream struct {
	srv  *httptest.Server
	hits atomic.Int64
	mu   sync.Mutex
	reqs []fimRequest
}

const fimUsage = `{"prompt_tokens":1200,"completion_tokens":10,"total_tokens":1210,"prompt_cache_hit_tokens":1000,"prompt_cache_miss_tokens":200}`

func newFIMUpstream(t *testing.T) *fimUpstream {
	u := &fimUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		u.reqs = append(u.reqs, fimRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})
		u.mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/completions") || strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			return
		}
		if stream, _ := body["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, chunk := range []string{
				`{"id":"cmpl-1","object":"text_completion","created":1,"model":"up","choices":[{"text":"    a, b = 0, 1\n","index":0,"logprobs":null,"finish_reason":null}]}`,
				`{"id":"cmpl-1","object":"text_completion","created":1,"model":"up","choices":[{"text":"    for _ in range(n):","index":0,"logprobs":null,"finish_reason":"stop"}]}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				w.(http.Flusher).Flush()
			}
			if so, _ := body["stream_options"].(map[string]any); so["include_usage"] == true {
				fmt.Fprintf(w, "data: %s\n\n", `{"id":"cmpl-1","object":"text_completion","created":1,"model":"up","choices":[],"usage":`+fimUsage+`}`)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cmpl-1","object":"text_completion","created":1,"model":"up","choices":[{"text":"    a, b = 0, 1\n","index":0,"logprobs":null,"finish_reason":"stop"}],"usage":`+fimUsage+`}`)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *fimUpstream) last() fimRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqs[len(u.reqs)-1]
}

// completionPost posts a completions request with extra headers.
func completionPost(t *testing.T, base, key, body string, headers ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/v1/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

const fimBody = `{"model":"ds-fim","prompt":"def fib(n):\n","suffix":"\n    return a\n","max_tokens":64,"temperature":0.2,"stop":["\n\n"],"x_vendor":{"k":1}}`

func TestCompletionsEndpoint(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	fim, plain := newFIMUpstream(t), newFIMUpstream(t)
	// The DeepSeek-style channel (base URL with /beta) serves completions; a
	// higher-priority openai channel without the flag and the user's own
	// anthropic channel (own tier: tried first if it could) serve the same
	// model but are never chosen.
	dsID := e.platformChannel(map[string]any{"name": "ds", "type": "openai", "baseUrl": fim.srv.URL + "/beta", "models": models("ds-fim", "fim-rr"),
		"config": map[string]any{"supportsCompletions": true}})
	e.platformChannel(map[string]any{"name": "plain", "type": "openai", "baseUrl": plain.srv.URL + "/v1", "priority": 10, "models": models("ds-fim", "chat-only")})
	e.channel(e.admin, map[string]any{"name": "claude", "type": "anthropic", "baseUrl": plain.srv.URL, "models": models("ds-fim")})
	if ch := e.mustDo(e.ops, http.MethodGet, "/api/channels/"+dsID, nil, 200); ch["config"].(map[string]any)["supportsCompletions"] != true {
		t.Fatalf("config = %v", ch["config"])
	}
	// Tiered sell price: prompt tokens (input + cache reads) above 1000 use the tier.
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "ds-fim", "inputPerM": "10", "outputPerM": "50", "cacheReadPerM": "1",
		"tiers": []map[string]any{{"aboveInputTokens": 1000, "inputPerM": "20", "outputPerM": "75", "cacheReadPerM": "2"}}}, 201)
	_, key := e.key(e.admin, map[string]any{"name": "fim"})

	t.Run("non-stream FIM request is passed through", func(t *testing.T) {
		code, body, raw := readBody(completionPost(t, base, key, fimBody, curlHeaders...))
		if code != 200 || body["object"] != "text_completion" || !strings.Contains(raw, `"prompt_cache_hit_tokens":1000`) {
			t.Fatalf("completions = %d %s", code, raw)
		}
		got := fim.last()
		if got.path != "/beta/completions" || got.body["model"] != "up-ds-fim" || got.body["prompt"] != "def fib(n):\n" || got.body["suffix"] != "\n    return a\n" ||
			got.body["max_tokens"] != float64(64) || got.body["x_vendor"] == nil || got.body["stop"].([]any)[0] != "\n\n" {
			t.Fatalf("upstream got %s %v", got.path, got.body)
		}
		if _, ok := got.body["stream_options"]; ok {
			t.Fatalf("stream_options added to a non-streaming request: %v", got.body)
		}
		l := e.lastLog(e.admin, "model=ds-fim")
		usage := l["usage"].(map[string]any)
		// Tier (1200 prompt tokens > 1000): 200 × 20/M + 1000 × 2/M + 10 × 75/M.
		if l["inbound"] != "openai.completions" || l["channelName"] != "ds" || l["stream"] != false || usage["input"] != float64(200) ||
			usage["cacheRead"] != float64(1000) || usage["output"] != float64(10) || usage["estimated"] != false || l["charge"] != "0.00675" ||
			l["priceTier"] != float64(1000) || l["client"].(map[string]any)["id"] != "curl" {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("stream: include_usage is forced and hidden from the client", func(t *testing.T) {
		body := strings.Replace(fimBody, `"max_tokens":64`, `"max_tokens":64,"stream":true`, 1)
		resp := completionPost(t, base, key, body)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("stream = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		var events []string
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				events = append(events, d)
			}
		}
		resp.Body.Close()
		if len(events) != 3 || events[2] != "[DONE]" || !strings.Contains(events[0], `"text":"    a, b = 0, 1\n"`) || strings.Contains(strings.Join(events, ""), "usage") {
			t.Fatalf("events = %q", events)
		}
		got := fim.last()
		if so := got.body["stream_options"].(map[string]any); so["include_usage"] != true || got.path != "/beta/completions" || got.body["suffix"] != "\n    return a\n" {
			t.Fatalf("upstream got %v", got.body)
		}
		l := e.lastLog(e.admin, "model=ds-fim")
		usage := l["usage"].(map[string]any)
		if l["stream"] != true || usage["cacheRead"] != float64(1000) || usage["input"] != float64(200) || l["charge"] != "0.00675" || l["ttftMs"] == nil {
			t.Fatalf("log = %v", l)
		}

		// The client asked for usage itself: the chunk is relayed.
		body = strings.Replace(fimBody, `"max_tokens":64`, `"max_tokens":64,"stream":true,"stream_options":{"include_usage":true}`, 1)
		_, _, raw := readBody(completionPost(t, base, key, body))
		if !strings.Contains(raw, `"prompt_cache_hit_tokens":1000`) {
			t.Fatalf("usage chunk hidden: %s", raw)
		}
	})

	t.Run("channels without completions support are never chosen", func(t *testing.T) {
		if n := plain.hits.Load(); n != 0 {
			t.Fatalf("plain / anthropic channels received %d requests", n)
		}
		code, body, raw := readBody(completionPost(t, base, key, `{"model":"chat-only","prompt":"x"}`))
		if code != 404 || body["error"].(map[string]any)["code"] != "model_not_found" || !strings.Contains(raw, "Completions support") {
			t.Fatalf("chat-only = %d %s", code, raw)
		}
		if plain.hits.Load() != 0 {
			t.Fatal("unsupported channel was tried")
		}
		// The same model still serves chat on the plain channel.
		if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, `{"model":"chat-only","messages":[{"role":"user","content":"hi"}]}`)); code != 200 {
			t.Fatalf("chat = %d %s", code, raw)
		}
		l := e.lastLog(e.admin, "model=chat-only&status=error")
		if l["inbound"] != "openai.completions" || l["statusCode"] != float64(404) {
			t.Fatalf("log = %v", l)
		}
		// Route preview lists the unsupported channels as skipped.
		pv := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "ds-fim", "inbound": "openai.completions"}, 200)
		cands := pv["candidates"].([]any)
		if len(cands) != 3 || cands[0].(map[string]any)["channelName"] != "ds" || cands[0].(map[string]any)["skipped"] != nil ||
			cands[0].(map[string]any)["upstreamDialect"] != "openai.completions" {
			t.Fatalf("preview = %v", cands)
		}
		for _, c := range cands[1:] {
			if s, _ := c.(map[string]any)["skipped"].(string); !strings.Contains(s, "Completions") {
				t.Fatalf("unsupported candidate = %v", c)
			}
		}
	})

	t.Run("key policy model restrictions apply", func(t *testing.T) {
		_, limited := e.key(e.admin, map[string]any{"name": "limited", "policy": map[string]any{"allowedModels": []string{"other"}}})
		code, _, raw := readBody(completionPost(t, base, limited, fimBody))
		if code != 403 {
			t.Fatalf("restricted key = %d %s", code, raw)
		}
	})

	t.Run("plaza lists Completions for marked models served by a completions channel", func(t *testing.T) {
		for _, model := range []string{"ds-fim", "chat-only"} {
			info := e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/"+model, map[string]any{"capabilities": map[string]any{"completions": true, "tools": true}}, 201)
			if c := info["capabilities"].(map[string]any); c["completions"] != true || c["embedding"] != false {
				t.Fatalf("info = %v", info)
			}
		}
		mine := e.mustDo(e.admin, http.MethodGet, "/api/plaza/mine", nil, 200)
		got := map[string]string{}
		for _, it := range mine["items"].([]any) {
			m := it.(map[string]any)
			got[m["model"].(string)] = strings.Join(toStrings(m["protocols"]), ",")
		}
		for model, want := range map[string]string{
			"ds-fim":    "openai.chat,openai.responses,anthropic.messages,openai.completions",
			"chat-only": "openai.chat,openai.responses,anthropic.messages", // marked, but no completions channel
			"fim-rr":    "openai.chat,openai.responses,anthropic.messages", // completions channel, not marked
		} {
			if got[model] != want {
				t.Errorf("%s protocols = %q, want %q", model, got[model], want)
			}
		}
	})

	t.Run("route rule and session affinity by path", func(t *testing.T) {
		fim2 := newFIMUpstream(t)
		e.platformChannel(map[string]any{"name": "ds2", "type": "openai", "baseUrl": fim2.srv.URL + "/beta", "models": models("fim-rr"),
			"config": map[string]any{"supportsCompletions": true}})
		e.mustDo(e.admin, http.MethodPost, "/api/admin/routes", map[string]any{"name": "fim rr", "match": map[string]any{"models": []string{"fim-rr"}},
			"strategy": "round_robin"}, 201)
		time.Sleep(50 * time.Millisecond)
		body := `{"model":"fim-rr","prompt":"x = ","suffix":"\n"}`
		served := func(headers ...string) string {
			t.Helper()
			a, b := fim.hits.Load(), fim2.hits.Load()
			if code, _, raw := readBody(completionPost(t, base, key, body, headers...)); code != 200 {
				t.Fatalf("fim-rr = %d %s", code, raw)
			}
			switch {
			case fim.hits.Load() > a:
				return "ds"
			case fim2.hits.Load() > b:
				return "ds2"
			}
			return ""
		}
		// The round-robin rule alternates between both completions channels.
		if x, y := served(), served(); x == y || x == "" || y == "" {
			t.Fatalf("round robin: %s, %s", x, y)
		}

		// A rule for /v1/completions keyed by a header pins the session; the
		// anchor never applies to completions, and the inject options are not
		// applied to completions bodies / headers.
		st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
		e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{"gateway": map[string]any{
			"affinity": map[string]any{"enabled": true, "max_entries": 1000, "default_ttl_seconds": 600, "rules": []any{
				map[string]any{"name": "fim anchor", "path_regex": []string{"^/v1/completions"}, "key_sources": []any{map[string]any{"type": "anchor"}}},
				map[string]any{"name": "fim session", "path_regex": []string{"^/v1/completions"}, "inject_prompt_cache_key": true, "inject_session_header": "Session_id",
					"key_sources": []any{map[string]any{"type": "request_header", "key": "X-Editor-Session"}}},
			}}}}}, 200)
		time.Sleep(20 * time.Millisecond)
		first := served("X-Editor-Session", "s-1")
		for i := 0; i < 3; i++ {
			if ch := served("X-Editor-Session", "s-1"); ch != first {
				t.Fatalf("session moved: %s → %s", first, ch)
			}
		}
		up := fim
		if first == "ds2" {
			up = fim2
		}
		if got := up.last(); got.body["prompt_cache_key"] != nil || got.header.Get("Session_id") != "" || got.body["suffix"] != "\n" {
			t.Fatalf("inject options applied to completions: %v %v", got.body, got.header)
		}
		l := e.lastLog(e.admin, "model=fim-rr")
		if l["affinityRule"] != "fim session" || l["affinity"] != "hit" {
			t.Fatalf("log = %v", l)
		}
		// Without the header only the anchor rule could apply: it never does.
		served()
		if l := e.lastLog(e.admin, "model=fim-rr"); l["affinityRule"] != nil {
			t.Fatalf("anchor applied to completions: %v", l)
		}
	})
}
