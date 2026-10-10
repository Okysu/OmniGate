package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func readAll(t *testing.T, s string) []*Event {
	t.Helper()
	r := NewSSEReader(strings.NewReader(s))
	var out []*Event
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
}

func TestSSEReader(t *testing.T) {
	evs := readAll(t, ": ping\n\nevent: message_start\ndata: {\"a\":1}\n\ndata: line1\ndata: line2\r\n\r\n\n\ndata: [DONE]")
	if len(evs) != 4 {
		t.Fatalf("got %d events", len(evs))
	}
	if !evs[0].IsComment() {
		t.Error("comment event not detected")
	}
	if evs[1].Name != "message_start" || string(evs[1].Data) != `{"a":1}` || string(evs[1].Raw) != "event: message_start\ndata: {\"a\":1}\n\n" {
		t.Errorf("event 1 = %+v raw=%q", evs[1], evs[1].Raw)
	}
	if string(evs[2].Data) != "line1\nline2" {
		t.Errorf("multi-line data = %q", evs[2].Data)
	}
	if string(evs[3].Data) != "[DONE]" || !bytes.HasSuffix(evs[3].Raw, []byte("\n\n")) {
		t.Errorf("unterminated trailing event = %+v raw=%q", evs[3], evs[3].Raw)
	}
}

func TestSSEReaderTooLarge(t *testing.T) {
	big := "data: " + strings.Repeat("x", MaxEventSize+10) + "\n\n"
	_, err := NewSSEReader(strings.NewReader(big)).Next()
	if !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseInfo(t *testing.T) {
	info, err := ParseInfo(OpenAIChat, []byte(`{"model":"m","stream":true,"max_completion_tokens":50,"stream_options":{"include_usage":true}}`))
	if err != nil || info.Model != "m" || !info.Stream || info.MaxTokens != 50 || !info.IncludeUsage {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if _, err := ParseInfo(Anthropic, []byte(`{"model":"m"}`)); err == nil {
		t.Fatal("anthropic without max_tokens must fail")
	}
	if _, err := ParseInfo(OpenAIChat, []byte(`{"stream":true}`)); err == nil {
		t.Fatal("missing model must fail")
	}
	// Huge limits are clamped (the balance estimate must not overflow).
	for _, body := range []string{
		`{"model":"m","max_output_tokens":9223372036854775807}`,
		`{"model":"m","max_tokens":1000001}`,
		`{"model":"m","max_completion_tokens":9223372036854775807,"max_tokens":5}`,
	} {
		if info, err := ParseInfo(OpenAIChat, []byte(body)); err != nil || info.MaxTokens != MaxTokensLimit {
			t.Fatalf("%s: info = %+v, %v", body, info, err)
		}
	}
	if info, _ := ParseInfo(Anthropic, []byte(`{"model":"m","max_tokens":9223372036854775807}`)); info.MaxTokens != MaxTokensLimit {
		t.Fatalf("anthropic max_tokens = %d", info.MaxTokens)
	}
}

func TestRewriteForPassthrough(t *testing.T) {
	out, err := RewriteForPassthrough(OpenAIChat, []byte(`{"model":"alias","stream":true,"x_vendor":{"k":1},"stream_options":{"include_usage":false}}`), "real", true)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["model"] != "real" || m["x_vendor"] == nil || m["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("rewritten = %s", out)
	}
}

func TestChatToAnthropicRequest(t *testing.T) {
	body := `{
	  "model":"gpt","stream":true,"max_tokens":100,"temperature":0.2,"stop":"END","user":"u1","n":1,"presence_penalty":0,
	  "messages":[
	    {"role":"system","content":"be nice"},
	    {"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},
	    {"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get","arguments":"{\"q\":1}"}}]},
	    {"role":"tool","tool_call_id":"call_1","content":"42"},
	    {"role":"user","content":"thanks"}
	  ],
	  "tools":[{"type":"function","function":{"name":"get","parameters":{"type":"object"}}}],
	  "tool_choice":"required","parallel_tool_calls":false
	}`
	out, warns, err := ChatToAnthropicRequest([]byte(body), "claude-x", Strict)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings %v", warns)
	}
	var r AnthropicRequest
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	if r.Model != "claude-x" || r.MaxTokens != 100 || !r.Stream || r.StopSequences[0] != "END" || r.Metadata.UserID != "u1" {
		t.Errorf("top-level = %s", out)
	}
	if !strings.Contains(string(r.System), "be nice") {
		t.Errorf("system = %s", r.System)
	}
	// user, assistant(tool_use), user(tool_result + "thanks" merged)
	if len(r.Messages) != 3 || r.Messages[1].Role != "assistant" || r.Messages[2].Role != "user" {
		t.Fatalf("messages = %s", out)
	}
	var last []AnthropicBlock
	_ = json.Unmarshal(r.Messages[2].Content, &last)
	if len(last) != 2 || last[0].Type != "tool_result" || last[0].ToolUseID != "call_1" || last[1].Text != "thanks" {
		t.Errorf("merged user message = %s", r.Messages[2].Content)
	}
	var first []AnthropicBlock
	_ = json.Unmarshal(r.Messages[0].Content, &first)
	if first[1].Type != "image" || first[1].Source.Type != "base64" || first[1].Source.MediaType != "image/png" {
		t.Errorf("image = %+v", first[1])
	}
	if r.ToolChoice.Type != "any" || r.ToolChoice.DisableParallelToolUse == nil || !*r.ToolChoice.DisableParallelToolUse {
		t.Errorf("tool_choice = %+v", r.ToolChoice)
	}
}

func TestStrictAndLenient(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"seed":7,"logprobs":true,"service_tier":"flex"}`)
	_, _, err := ChatToAnthropicRequest(body, "c", Strict)
	var ce *ConvertError
	if !errors.As(err, &ce) || ce.Class != "unsupported_parameter" || strings.Join(ce.Fields, ",") != "logprobs,seed" {
		t.Fatalf("strict err = %v", err)
	}
	_, warns, err := ChatToAnthropicRequest(body, "c", Lenient)
	if err != nil || strings.Join(warns, ",") != "logprobs,seed,service_tier" {
		t.Fatalf("lenient warns = %v err = %v", warns, err)
	}
}

func TestAnthropicToChatRequest(t *testing.T) {
	body := `{
	  "model":"claude","max_tokens":256,"stream":true,
	  "system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],
	  "messages":[
	    {"role":"user","content":"hello"},
	    {"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"calling"},{"type":"tool_use","id":"toolu_1","name":"f","input":{"a":1}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"ok"}]},{"type":"text","text":"next"}]}
	  ],
	  "tools":[{"name":"f","input_schema":{"type":"object"}}],
	  "tool_choice":{"type":"tool","name":"f"},
	  "thinking":{"type":"disabled"}
	}`
	out, warns, err := AnthropicToChatRequest([]byte(body), "gpt-x", "max_completion_tokens", Strict)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(warns, ",") != "cache_control,thinking" {
		t.Errorf("warnings = %v", warns)
	}
	var r ChatRequest
	_ = json.Unmarshal(out, &r)
	if r.Model != "gpt-x" || r.MaxCompletionTokens == nil || *r.MaxCompletionTokens != 256 || r.StreamOptions == nil || !r.StreamOptions.IncludeUsage {
		t.Errorf("top-level = %s", out)
	}
	roles := []string{}
	for _, m := range r.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,user" {
		t.Fatalf("roles = %v (%s)", roles, out)
	}
	if r.Messages[2].ToolCalls[0].Function.Arguments != `{"a":1}` || r.Messages[3].ToolCallID != "toolu_1" {
		t.Errorf("tool round trip = %s", out)
	}
	if string(r.ToolChoice) != `{"function":{"name":"f"},"type":"function"}` {
		t.Errorf("tool_choice = %s", r.ToolChoice)
	}
	// top_k has no OpenAI equivalent -> strict error
	_, _, err = AnthropicToChatRequest([]byte(`{"model":"c","max_tokens":5,"messages":[{"role":"user","content":"x"}],"top_k":5}`), "g", "max_tokens", Strict)
	var ce *ConvertError
	if !errors.As(err, &ce) || strings.Join(ce.Fields, ",") != "top_k" {
		t.Fatalf("err = %v", err)
	}
}

func TestThinkingMapping(t *testing.T) {
	for budget, want := range map[int64]string{1024: "low", 8000: "medium", 32000: "high"} {
		body := fmt.Sprintf(`{"model":"c","max_tokens":40000,"messages":[{"role":"user","content":"x"}],"thinking":{"type":"enabled","budget_tokens":%d}}`, budget)
		out, _, err := AnthropicToChatRequest([]byte(body), "g", "max_tokens", Strict)
		var r ChatRequest
		_ = json.Unmarshal(out, &r)
		if err != nil || r.ReasoningEffort != want {
			t.Errorf("budget %d -> %q (%v), want %q", budget, r.ReasoningEffort, err, want)
		}
	}
	out, warns, err := ChatToAnthropicRequest([]byte(`{"model":"g","max_tokens":1000,"temperature":0.3,"reasoning_effort":"medium","messages":[{"role":"user","content":"x"}]}`), "c", Strict)
	var a AnthropicRequest
	_ = json.Unmarshal(out, &a)
	if err != nil || a.Thinking == nil || a.Thinking.BudgetTokens != 8192 || a.MaxTokens != 9192 || a.Temperature != nil {
		t.Fatalf("anthropic = %s err=%v", out, err)
	}
	if strings.Join(warns, ",") != "max_tokens(raised_for_thinking),temperature(thinking)" {
		t.Errorf("warnings = %v", warns)
	}
	out, _, _ = ChatToAnthropicRequest([]byte(`{"model":"g","reasoning_effort":"minimal","messages":[{"role":"user","content":"x"}]}`), "c", Strict)
	if strings.Contains(string(out), "thinking") {
		t.Errorf("minimal effort must not enable thinking: %s", out)
	}
}

func TestReasoningFieldAliases(t *testing.T) {
	// Some providers send both reasoning_content and reasoning with identical text.
	up := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"ab\",\"reasoning\":\"ab\"}}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"cd\"}}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	p, _ := NewStreamProcessor(Anthropic, OpenAIChat, "m", false)
	out := run(t, p, up)
	thinking := ""
	for _, ev := range readAll(t, out) {
		var m struct {
			Delta struct{ Type, Thinking string }
		}
		_ = json.Unmarshal(ev.Data, &m)
		if m.Delta.Type == "thinking_delta" {
			thinking += m.Delta.Thinking
		}
	}
	if thinking != "abcd" {
		t.Errorf("thinking = %q", thinking)
	}
}

func TestResponseConversions(t *testing.T) {
	chat := `{"id":"abc","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"hi","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"x\":2}"}}]},"finish_reason":"tool_calls"}],
	  "usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":30}}}`
	out, u, err := ChatToAnthropicResponse([]byte(chat), "alias")
	if err != nil {
		t.Fatal(err)
	}
	if u != (Usage{Input: 70, Output: 20, CacheRead: 30}) {
		t.Errorf("usage = %+v", u)
	}
	var a AnthropicResponse
	_ = json.Unmarshal(out, &a)
	if a.Model != "alias" || *a.StopReason != "tool_use" || len(a.Content) != 2 || a.Content[1].Type != "tool_use" || string(a.Content[1].Input) != `{"x":2}` {
		t.Errorf("anthropic = %s", out)
	}
	if a.Usage.InputTokens != 70 || a.Usage.CacheReadInputTokens != 30 {
		t.Errorf("anthropic usage = %+v", a.Usage)
	}

	back, u2, err := AnthropicToChatResponse(out, "alias")
	if err != nil {
		t.Fatal(err)
	}
	if u2 != u {
		t.Errorf("round-trip usage %+v != %+v", u2, u)
	}
	var c ChatResponse
	_ = json.Unmarshal(back, &c)
	if *c.Choices[0].FinishReason != "tool_calls" || c.Usage.PromptTokens != 100 || c.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"x":2}` {
		t.Errorf("chat = %s", back)
	}
}

func run(t *testing.T, p StreamProcessor, upstream string) string {
	t.Helper()
	var out bytes.Buffer
	for _, ev := range readAll(t, upstream) {
		b, err := p.Process(ev)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(b)
	}
	out.Write(p.Finish())
	return out.String()
}

func TestChatPassthroughHidesForcedUsage(t *testing.T) {
	up := "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hey\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"1\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
	p, _ := NewStreamProcessor(OpenAIChat, OpenAIChat, "m", false)
	out := run(t, p, up)
	if strings.Contains(out, "usage") || !strings.Contains(out, "[DONE]") {
		t.Errorf("out = %q", out)
	}
	if u, ok := p.Usage(); !ok || u.Input != 5 || u.Output != 2 || !p.Done() {
		t.Errorf("usage = %+v %v", u, ok)
	}
	p2, _ := NewStreamProcessor(OpenAIChat, OpenAIChat, "m", true)
	if out := run(t, p2, up); !strings.Contains(out, "usage") {
		t.Error("client asked for usage but it was hidden")
	}
}

func TestChatStreamToAnthropic(t *testing.T) {
	up := `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"id":"x","choices":[{"index":0,"delta":{"content":"Hel"}}]}

data: {"id":"x","choices":[{"index":0,"delta":{"content":"lo"}}]}

data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"f","arguments":""}}]}}]}

data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]}}]}

data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}

data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"x","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7}}

data: [DONE]

`
	p, _ := NewStreamProcessor(Anthropic, OpenAIChat, "claude-alias", false)
	out := run(t, p, up)
	var types []string
	text, args := "", ""
	var stop string
	var outTokens float64
	for _, ev := range readAll(t, out) {
		var m map[string]any
		if err := json.Unmarshal(ev.Data, &m); err != nil {
			t.Fatalf("bad event %q", ev.Data)
		}
		if m["type"] != ev.Name {
			t.Errorf("event name %q != type %v", ev.Name, m["type"])
		}
		types = append(types, ev.Name)
		if d, ok := m["delta"].(map[string]any); ok {
			if d["type"] == "text_delta" {
				text += d["text"].(string)
			}
			if d["type"] == "input_json_delta" {
				args += d["partial_json"].(string)
			}
			if sr, ok := d["stop_reason"].(string); ok {
				stop = sr
				outTokens = m["usage"].(map[string]any)["output_tokens"].(float64)
			}
		}
	}
	want := "message_start,content_block_start,content_block_delta,content_block_delta,content_block_stop,content_block_start,content_block_delta,content_block_delta,content_block_stop,message_delta,message_stop"
	if strings.Join(types, ",") != want {
		t.Fatalf("events:\n got %s\nwant %s", strings.Join(types, ","), want)
	}
	if text != "Hello" || args != `{"a":1}` || stop != "tool_use" || outTokens != 7 {
		t.Errorf("text=%q args=%q stop=%q out=%v", text, args, stop, outTokens)
	}
}

func TestAnthropicStreamToChat(t *testing.T) {
	up := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":9,"output_tokens":1,"cache_read_input_tokens":4}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type":"ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"f","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"k\":true}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":15}}

event: message_stop
data: {"type":"message_stop"}

`
	p, _ := NewStreamProcessor(OpenAIChat, Anthropic, "gpt-alias", true)
	out := run(t, p, up)
	evs := readAll(t, out)
	if string(evs[len(evs)-1].Data) != "[DONE]" {
		t.Fatalf("missing [DONE]: %s", out)
	}
	var content, args, finish string
	var usage *chatUsage
	for _, ev := range evs[:len(evs)-1] {
		var c ChatResponse
		if err := json.Unmarshal(ev.Data, &c); err != nil {
			t.Fatalf("bad chunk %s", ev.Data)
		}
		if c.Model != "gpt-alias" || c.Object != "chat.completion.chunk" {
			t.Errorf("chunk = %s", ev.Data)
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		for _, ch := range c.Choices {
			if ch.Delta.Content != nil {
				content += *ch.Delta.Content
			}
			for _, tc := range ch.Delta.ToolCalls {
				args += tc.Function.Arguments
			}
			if ch.FinishReason != nil {
				finish = *ch.FinishReason
			}
		}
	}
	if content != "Hi" || args != `{"k":true}` || finish != "tool_calls" {
		t.Errorf("content=%q args=%q finish=%q", content, args, finish)
	}
	if usage == nil || usage.PromptTokens != 13 || usage.CompletionTokens != 15 {
		t.Errorf("usage = %+v", usage)
	}
	if u, _ := p.Usage(); u != (Usage{Input: 9, Output: 15, CacheRead: 4}) {
		t.Errorf("metered = %+v", u)
	}
}

func TestMalformedStreamJSON(t *testing.T) {
	p, _ := NewStreamProcessor(Anthropic, OpenAIChat, "m", false)
	if _, err := p.Process(&Event{Data: []byte("{not json")}); err == nil {
		t.Fatal("expected error for malformed upstream chunk")
	}
	// Passthrough forwards malformed data untouched rather than failing.
	pt, _ := NewStreamProcessor(OpenAIChat, OpenAIChat, "m", false)
	if b, err := pt.Process(&Event{Data: []byte("{not json"), Raw: []byte("data: {not json\n\n")}); err != nil || len(b) == 0 {
		t.Fatalf("passthrough = %q %v", b, err)
	}
}

func TestRedactAndErrors(t *testing.T) {
	msg := UpstreamErrorMessage([]byte(`{"error":{"message":"Incorrect API key provided: sk-proj-abcdefghijklmnop1234. Bearer abcdefghijk"}}`))
	if strings.Contains(msg, "sk-proj") || strings.Contains(msg, "abcdefghijk") {
		t.Fatalf("not redacted: %s", msg)
	}
	b := EncodeError(Anthropic, NewError(ErrModelNotFound, "nope"))
	if string(b) != `{"error":{"message":"nope","type":"not_found_error"},"type":"error"}` {
		t.Errorf("anthropic error = %s", b)
	}
	if NewError(ErrInsufficientBalance, "").Status != 402 {
		t.Error("insufficient balance must be 402")
	}
}

func TestAnthropicInlineSystemMessage(t *testing.T) {
	body := `{"model":"c","max_tokens":10,"messages":[{"role":"user","content":"a"},{"role":"system","content":[{"type":"text","text":"reminder"}]},{"role":"user","content":"b"}]}`
	out, _, err := AnthropicToChatRequest([]byte(body), "g", "max_tokens", Strict)
	if err != nil {
		t.Fatal(err)
	}
	var r ChatRequest
	_ = json.Unmarshal(out, &r)
	if len(r.Messages) != 3 || r.Messages[1].Role != "system" || string(r.Messages[1].Content) != `"reminder"` {
		t.Fatalf("messages = %s", out)
	}
}

func TestClaudeCodeStyleRequest(t *testing.T) {
	uid := `{"device_id":"` + strings.Repeat("a", 64) + `","session_id":"s"}`
	body := fmt.Sprintf(`{"model":"c","max_tokens":32000,"stream":true,"metadata":{"user_id":%q},
	  "thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"max"},
	  "context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"safeguards":[{"x":1}],
	  "messages":[{"role":"user","content":"hi"},{"role":"system","content":"reminder"}]}`, uid)
	out, warns, err := AnthropicToChatRequest([]byte(body), "g", "max_tokens", Strict)
	if err != nil {
		t.Fatal(err)
	}
	var r ChatRequest
	_ = json.Unmarshal(out, &r)
	if r.ReasoningEffort != "high" || !strings.HasPrefix(r.User, "og-") || len(r.User) != 35 {
		t.Fatalf("request = %s", out)
	}
	if strings.Join(warns, ",") != "context_management,safeguards" {
		t.Fatalf("warnings = %v", warns)
	}
	if _, _, err := AnthropicToChatRequest([]byte(`{"model":"c","max_tokens":5,"messages":[{"role":"user","content":"x"}],"output_config":{"format":{}}}`), "g", "max_tokens", Strict); err == nil {
		t.Fatal("unknown output_config keys must be rejected in strict mode")
	}
}

func TestSetPromptCacheKey(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"absent":  {`{"model":"m","messages":[{"role":"user","content":"a  b"}],"stream":true}`, `{"prompt_cache_key":"og-1","model":"m","messages":[{"role":"user","content":"a  b"}],"stream":true}`},
		"spaces":  {"  {\n \"model\": \"m\"\n}", "{\"prompt_cache_key\":\"og-1\",\n \"model\": \"m\"\n}"},
		"empty":   {`{}`, `{"prompt_cache_key":"og-1"}`},
		"blank":   {`{ }`, `{"prompt_cache_key":"og-1" }`},
		"client":  {`{"model":"m","prompt_cache_key":"mine"}`, `{"model":"m","prompt_cache_key":"mine"}`},
		"number":  {`{"prompt_cache_key":7}`, `{"prompt_cache_key":7}`},
		"empty v": {`{"prompt_cache_key":"","model":"m"}`, `{"model":"m","prompt_cache_key":"og-1"}`},
		"null":    {`{"prompt_cache_key":null}`, `{"prompt_cache_key":"og-1"}`},
		"nested":  {`{"metadata":{"prompt_cache_key":"x"}}`, `{"prompt_cache_key":"og-1","metadata":{"prompt_cache_key":"x"}}`},
	} {
		out, err := SetPromptCacheKey([]byte(tc.in), "og-1")
		if err != nil || string(out) != tc.want {
			t.Errorf("%s: %s %v, want %s", name, out, err, tc.want)
		}
		if !json.Valid(out) {
			t.Errorf("%s: invalid JSON %s", name, out)
		}
	}
	if _, err := SetPromptCacheKey([]byte(`[1]`), "og-1"); err == nil {
		t.Error("non-object accepted")
	}
}
