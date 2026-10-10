package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompletionsParseInfo(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","prompt":"def fib(n):","suffix":"    return a","max_tokens":64,"stream":true,"stream_options":{"include_usage":true}}`,
		`{"model":"m","prompt":["a","b"],"max_tokens":64,"stream":true,"stream_options":{"include_usage":true}}`,
		`{"model":"m","prompt":[[1,2,3],[4]],"max_tokens":64,"stream":true,"stream_options":{"include_usage":true}}`,
		`{"model":"m","prompt":[1,2,3],"max_tokens":64,"stream":true,"stream_options":{"include_usage":true},"echo":true,"best_of":2,"logit_bias":{"50256":-100}}`,
	} {
		info, err := ParseInfo(OpenAICompletions, []byte(body))
		if err != nil || info.Model != "m" || !info.Stream || info.MaxTokens != 64 || !info.IncludeUsage {
			t.Fatalf("%s: info = %+v, %v", body, info, err)
		}
	}
	if _, err := ParseInfo(OpenAICompletions, []byte(`{"prompt":"x"}`)); err == nil {
		t.Fatal("missing model must fail")
	}
	if !OpenAIOnly(OpenAICompletions) {
		t.Fatal("completions are passthrough-only")
	}
}

func TestCompletionsRewriteForPassthrough(t *testing.T) {
	body := `{"model":"alias","prompt":"def fib(n):","suffix":"\n    return a","max_tokens":64,"temperature":0.2,"top_p":1,"n":1,` +
		`"logprobs":null,"echo":false,"stop":["\n\n"],"presence_penalty":0,"frequency_penalty":0,"best_of":1,"user":"u1","seed":7,"x_vendor":{"k":1}}`
	out, err := RewriteForPassthrough(OpenAICompletions, []byte(body), "deepseek-v4-pro", false)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	_ = json.Unmarshal(out, &got)
	_ = json.Unmarshal([]byte(body), &want)
	want["model"] = "deepseek-v4-pro"
	if b1, _ := json.Marshal(got); string(b1) != mustJSON(want) {
		t.Fatalf("rewritten = %s", out)
	}
	if _, ok := got["stream_options"]; ok {
		t.Fatal("stream_options added to a non-streaming request")
	}

	// Streaming: include_usage is forced (other stream_options kept); token
	// array prompts pass through unchanged.
	out, err = RewriteForPassthrough(OpenAICompletions, []byte(`{"model":"alias","prompt":[[1,2],[3]],"stream":true,"stream_options":{"x":1}}`), "real", true)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	_ = json.Unmarshal(out, &got)
	so := got["stream_options"].(map[string]any)
	if got["model"] != "real" || so["include_usage"] != true || so["x"] != float64(1) || mustJSON(got["prompt"]) != `[[1,2],[3]]` {
		t.Fatalf("rewritten = %s", out)
	}
}

func TestCompletionUsage(t *testing.T) {
	cases := []struct {
		name, body string
		want       Usage
	}{
		{"openai", `{"object":"text_completion","choices":[{"text":"x"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":64}}}`,
			Usage{Input: 36, CacheRead: 64, Output: 20}},
		{"deepseek", `{"choices":[{"text":"x"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":36}}`,
			Usage{Input: 36, CacheRead: 64, Output: 20}},
		{"deepseek without prompt_tokens", `{"usage":{"completion_tokens":5,"prompt_cache_hit_tokens":10,"prompt_cache_miss_tokens":30}}`,
			Usage{Input: 30, CacheRead: 10, Output: 5}},
		{"cached_tokens wins", `{"usage":{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":50},"prompt_cache_hit_tokens":64}}`,
			Usage{Input: 50, CacheRead: 50, Output: 1}},
		{"plain", `{"usage":{"prompt_tokens":7,"completion_tokens":3}}`, Usage{Input: 7, Output: 3}},
	}
	for _, c := range cases {
		u, ok := UsageFromCompletionResponse([]byte(c.body))
		if !ok || u != c.want {
			t.Errorf("%s: usage = %+v %v, want %+v", c.name, u, ok, c.want)
		}
	}
	if _, ok := UsageFromCompletionResponse([]byte(`{"choices":[]}`)); ok {
		t.Error("no usage must report !ok")
	}
}

func TestCompletionStreamPassthrough(t *testing.T) {
	up := "data: {\"id\":\"c1\",\"object\":\"text_completion\",\"choices\":[{\"text\":\"    a, b\",\"index\":0,\"logprobs\":null,\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"text_completion\",\"choices\":[{\"text\":\" = 0, 1\",\"index\":0,\"logprobs\":null,\"finish_reason\":\"stop\"}]}\n\n" +
		": keep-alive\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"text_completion\",\"choices\":[],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":6,\"prompt_cache_hit_tokens\":32,\"prompt_cache_miss_tokens\":8}}\n\n" +
		"data: [DONE]\n\n"
	p, err := NewStreamProcessor(OpenAICompletions, OpenAICompletions, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	out := run(t, p, up)
	if strings.Contains(out, "usage") || !strings.Contains(out, `"text":"    a, b"`) || !strings.Contains(out, ": keep-alive") || !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Errorf("out = %q", out)
	}
	if u, ok := p.Usage(); !ok || u != (Usage{Input: 8, CacheRead: 32, Output: 6}) || !p.Done() || p.OutputBytes() != len("    a, b")+len(" = 0, 1") {
		t.Errorf("usage = %+v %v done=%v out=%d", u, ok, p.Done(), p.OutputBytes())
	}
	// The client asked for usage: the chunk is relayed unchanged.
	p2, _ := NewStreamProcessor(OpenAICompletions, OpenAICompletions, "m", true)
	if out := run(t, p2, up); !strings.Contains(out, `"prompt_cache_hit_tokens":32`) {
		t.Error("client asked for usage but it was hidden")
	}
	// Usage on the last text chunk (choices present) is never hidden.
	p3, _ := NewStreamProcessor(OpenAICompletions, OpenAICompletions, "m", false)
	last := "data: {\"choices\":[{\"text\":\"x\",\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
	if out := run(t, p3, last); !strings.Contains(out, "usage") {
		t.Errorf("out = %q", out)
	}
	if _, err := NewStreamProcessor(OpenAIChat, OpenAICompletions, "m", false); err == nil {
		t.Error("completions are never converted")
	}
}
