package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func canonicalEvents(t *testing.T, raw string) []CanonicalEvent {
	t.Helper()
	var evs []CanonicalEvent
	if err := json.Unmarshal([]byte(raw), &evs); err != nil {
		t.Fatal(err)
	}
	return evs
}

const canonicalSample = `[
  {"type":"delta","reasoning":"想一想"},
  {"type":"delta","content":"你好"},
  {"type":"delta","toolCalls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]},
  {"type":"delta","toolCalls":[{"index":0,"function":{"arguments":"\"北京\"}"}}]},
  {"type":"finish","reason":"tool_calls"},
  {"type":"usage","usage":{"prompt_tokens":10,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3}}}
]`

func runCanonical(t *testing.T, client string, includeUsage bool) (string, *CanonicalStream) {
	t.Helper()
	s, err := NewCanonicalStream(client, "m", includeUsage)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Events(canonicalEvents(t, canonicalSample))
	if err != nil {
		t.Fatal(err)
	}
	tail, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Done() {
		t.Fatal("stream not done")
	}
	u, ok := s.Usage()
	if !ok || u.Input != 7 || u.CacheRead != 3 || u.Output != 4 {
		t.Fatalf("usage = %+v %v", u, ok)
	}
	return string(out) + string(tail), s
}

func TestCanonicalStreamToChat(t *testing.T) {
	out, _ := runCanonical(t, OpenAIChat, true)
	for _, want := range []string{`"role":"assistant"`, `"reasoning_content":"想一想"`, `"content":"你好"`, `"name":"get_weather"`,
		`"finish_reason":"tool_calls"`, `"prompt_tokens":10`, "data: [DONE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	out, _ = runCanonical(t, OpenAIChat, false)
	if strings.Contains(out, `"prompt_tokens"`) {
		t.Errorf("usage chunk shown to a client that did not ask:\n%s", out)
	}
}

func TestCanonicalStreamToAnthropicAndResponses(t *testing.T) {
	out, _ := runCanonical(t, Anthropic, false)
	for _, want := range []string{"event: message_start", `"thinking":"想一想"`, `"text":"你好"`, `"type":"tool_use"`,
		`"partial_json":"\"北京\"}"`, `"stop_reason":"tool_use"`, `"output_tokens":4`, "event: message_stop"} {
		if !strings.Contains(out, want) {
			t.Errorf("anthropic missing %s in\n%s", want, out)
		}
	}
	out, _ = runCanonical(t, OpenAIResponses, false)
	for _, want := range []string{"response.created", "response.output_text.delta", "response.function_call_arguments.delta",
		`"arguments":"{\"city\":\"北京\"}"`, "response.completed", `"input_tokens":10`} {
		if !strings.Contains(out, want) {
			t.Errorf("responses missing %s in\n%s", want, out)
		}
	}
}

func TestCanonicalStreamErrorsAndImplicitFinish(t *testing.T) {
	s, _ := NewCanonicalStream(OpenAIChat, "m", false)
	out, err := s.Events(canonicalEvents(t, `[{"type":"delta","content":"a"},{"type":"error","message":"boom","status":429}]`))
	ce, ok := err.(*CanonicalError)
	if !ok || ce.Status != 429 || ce.Message != "boom" || !strings.Contains(string(out), `"content":"a"`) {
		t.Fatalf("error event = %v %s", err, out)
	}
	if _, err := s.Events(canonicalEvents(t, `[{"type":"bogus"}]`)); err == nil {
		t.Fatal("unknown event accepted")
	}
	s, _ = NewCanonicalStream(Anthropic, "m", false)
	if _, err := s.Events(canonicalEvents(t, `[{"type":"delta","content":"hi"}]`)); err != nil {
		t.Fatal(err)
	}
	tail, _ := s.Finish()
	if !strings.Contains(string(tail), `"stop_reason":"end_turn"`) || !s.Done() {
		t.Fatalf("implicit finish = %s", tail)
	}
}

func TestNormalizeCanonicalResponse(t *testing.T) {
	out, u, ok, err := NormalizeCanonicalResponse([]byte(`{"id":"r1","choices":[{"message":{"content":"hi","tool_calls":[{"function":{"name":"f","arguments":"{}"}}]}}],
		"usage":{"prompt_tokens":3,"completion_tokens":2}}`), "m")
	if err != nil || !ok || u.Input != 3 || u.Output != 2 {
		t.Fatalf("%v %v %+v", err, ok, u)
	}
	var r ChatResponse
	_ = json.Unmarshal(out, &r)
	if r.Object != "chat.completion" || r.Model != "m" || !strings.HasPrefix(r.ID, "chatcmpl-") || *r.Choices[0].FinishReason != "tool_calls" ||
		r.Choices[0].Message.Role != "assistant" || r.Choices[0].Message.ToolCalls[0].Type != "function" || r.Choices[0].Message.ToolCalls[0].ID == "" {
		t.Fatalf("normalized = %s", out)
	}
	if _, _, _, err := NormalizeCanonicalResponse([]byte(`{"error":{"status":401,"message":"bad key"}}`), "m"); err == nil {
		t.Fatal("error not reported")
	} else if ce := err.(*CanonicalError); ce.Status != 401 {
		t.Fatalf("status = %d", ce.Status)
	}
	if _, _, _, err := NormalizeCanonicalResponse([]byte(`{"choices":[]}`), "m"); err == nil {
		t.Fatal("empty choices accepted")
	}
	b, _, _, _ := NormalizeCanonicalResponse([]byte(`{"choices":[{"message":{"content":"x"}}]}`), "m")
	conv, _, err := ConvertResponse(Anthropic, OpenAIChat, b, "m")
	if err != nil || !strings.Contains(string(conv), `"text":"x"`) {
		t.Fatalf("convert = %s %v", conv, err)
	}
}
