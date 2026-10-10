package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"omnigate/internal/plugin/engine"
)

// The bundled community.body-rewrite plugin rewrites large requests (long
// contexts, base64 images) within the production hook budget, without
// dropping messages.
func TestBodyRewriteLargeBodyWithinBudget(t *testing.T) {
	bundled, err := BundledFiles()
	if err != nil {
		t.Fatal(err)
	}
	files := bundled["community.body-rewrite"]
	b := Build(files, false)
	if !b.OK {
		t.Fatalf("build: %+v", b.Diagnostics)
	}
	s := testService()
	prog, err := s.engine.Program("test-body-rewrite", b.bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer s.engine.Drop("test-body-rewrite")
	l := &Loaded{Manifest: b.Manifest, prog: prog, svc: s, Version: b.Manifest.Version}

	const n = 4000
	msgs := make([]any, 0, n+2)
	msgs = append(msgs, map[string]any{"role": "developer", "content": "规则"})
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": fmt.Sprintf("问题 %d %s", i, strings.Repeat("x", 400))})
		} else {
			msgs = append(msgs, map[string]any{"role": "assistant", "content": strings.Repeat("y", 400), "reasoning_content": strings.Repeat("z", 200)})
		}
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + strings.Repeat("A", 2<<20)}},
		map[string]any{"type": "text", "text": "描述这张图"},
	}})
	body, _ := json.Marshal(map[string]any{"model": "qwen3-vl:8b", "stream": true, "messages": msgs, "max_tokens": 100000,
		"parallel_tool_calls": true, "stream_options": map[string]any{"include_usage": true}})
	if len(body) < 4<<20 {
		t.Fatalf("body is only %d bytes", len(body))
	}
	cfg := map[string]any{"modelRegex": "^qwen3", "thinking": "off", "thinkingStyle": "qwen_soft_switch", "stripReasoningContent": true,
		"developerToSystem": true, "removeFields": "parallel_tool_calls, stream_options", "maxTokensCap": 4096,
		"mergeBody": `{"chat_template_kwargs": {"enable_thinking": false}, "top_k": 20}`}
	// The hook path of Loaded.RunHooks with the production budget; only the
	// race detector, which slows the host's own JSON conversion of the body
	// several times over (a no-op hook takes ~0.9 s here), gets the maximum.
	ch := ChannelEnv{Name: "测试", BaseURL: "http://127.0.0.1:11434/v1", Config: cfg}
	req := UpstreamRequest{Dialect: "openai.chat", Path: "/chat/completions", Headers: map[string]string{}, Body: body}
	base := HookTimeout
	if raceDetector {
		base = 2 * time.Second // the per-hook maximum
	}
	start := time.Now()
	r, err := l.prog.RunHooks(context.Background(), l.env(ch, "", nil), []string{"transformRequest"}, req, base)
	if err != nil {
		t.Fatalf("hook failed on a %d-byte body: %v", len(body), err)
	}
	t.Logf("%d-byte body rewritten in %v (budget %v)", len(body), time.Since(start), engine.HookBudget(base, len(body)))
	var out UpstreamRequest
	if err := json.Unmarshal(r.Output, &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Model             string           `json:"model"`
		Stream            bool             `json:"stream"`
		MaxTokens         int              `json:"max_tokens"`
		TopK              int              `json:"top_k"`
		ParallelToolCalls *bool            `json:"parallel_tool_calls"`
		Messages          []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "qwen3-vl:8b" || !got.Stream || got.MaxTokens != 4096 || got.TopK != 20 || got.ParallelToolCalls != nil || len(got.Messages) != n+2 {
		t.Fatalf("model=%q stream=%v max_tokens=%d top_k=%d parallel=%v messages=%d", got.Model, got.Stream, got.MaxTokens, got.TopK, got.ParallelToolCalls, len(got.Messages))
	}
	if got.Messages[0]["role"] != "system" || got.Messages[2]["reasoning_content"] != nil {
		t.Fatalf("developer / reasoning not rewritten: %v %v", got.Messages[0]["role"], got.Messages[2]["reasoning_content"])
	}
	last := got.Messages[n+1]["content"].([]any)
	if text := last[1].(map[string]any)["text"]; text != "描述这张图 /no_think" {
		t.Fatalf("soft switch: %v", text)
	}
}
