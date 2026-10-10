package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestResponsesToChatRequest(t *testing.T) {
	body := `{"model":"r","instructions":"be brief","max_output_tokens":300,"stream":true,
	  "reasoning":{"effort":"low"},"text":{"format":{"type":"json_schema","name":"x","schema":{"type":"object"}}},
	  "input":[
	    {"role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"https://img/x.png"}]},
	    {"type":"reasoning","id":"rs_1","summary":[]},
	    {"type":"function_call","call_id":"c1","name":"f","arguments":"{\"a\":1}"},
	    {"type":"function_call","call_id":"c2","name":"g","arguments":"{}"},
	    {"type":"function_call_output","call_id":"c1","output":"one"},
	    {"type":"function_call_output","call_id":"c2","output":"two"},
	    {"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
	  ],
	  "tools":[{"type":"function","name":"f","parameters":{"type":"object"}}],
	  "tool_choice":{"type":"function","name":"f"},"store":true}`
	out, warns, err := ResponsesToChatRequest([]byte(body), "up", "max_tokens", Strict)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(warns, ",") != "input[].reasoning" {
		t.Errorf("warnings = %v", warns)
	}
	var r map[string]any
	_ = json.Unmarshal(out, &r)
	msgs := r["messages"].([]any)
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,tool,assistant" {
		t.Fatalf("roles = %v (%s)", roles, out)
	}
	if calls := msgs[2].(map[string]any)["tool_calls"].([]any); len(calls) != 2 {
		t.Errorf("parallel calls not merged: %v", calls)
	}
	if r["max_tokens"].(float64) != 300 || r["reasoning_effort"] != "low" || r["response_format"].(map[string]any)["type"] != "json_schema" {
		t.Errorf("top-level = %s", out)
	}
	if r["tool_choice"].(map[string]any)["function"].(map[string]any)["name"] != "f" {
		t.Errorf("tool_choice = %v", r["tool_choice"])
	}
	_, _, err = ResponsesToChatRequest([]byte(`{"model":"r","input":"hi","previous_response_id":"resp_1"}`), "up", "max_tokens", Lenient)
	var ce *ConvertError
	if !errors.As(err, &ce) || ce.Fields[0] != "previous_response_id" {
		t.Fatalf("previous_response_id must always fail: %v", err)
	}
}

func TestChatToResponsesRequest(t *testing.T) {
	body := `{"model":"c","max_completion_tokens":50,"reasoning_effort":"high","stream":true,"stream_options":{"include_usage":true},
	  "messages":[{"role":"system","content":"sys"},{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA","detail":"low"}}]},
	    {"role":"assistant","content":"ok","tool_calls":[{"id":"t1","type":"function","function":{"name":"f","arguments":""}}]},
	    {"role":"tool","tool_call_id":"t1","content":"res"}],
	  "tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"}}}],"tool_choice":"required",
	  "response_format":{"type":"json_object"}}`
	out, _, err := ChatToResponsesRequest([]byte(body), "gpt-6", Strict)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	_ = json.Unmarshal(out, &r)
	if r["store"] != false || r["max_output_tokens"].(float64) != 50 || r["reasoning"].(map[string]any)["effort"] != "high" || r["tool_choice"] != "required" {
		t.Errorf("top-level = %s", out)
	}
	items := r["input"].([]any)
	types := []string{}
	for _, it := range items {
		m := it.(map[string]any)
		if tt, ok := m["type"].(string); ok {
			types = append(types, tt)
		} else {
			types = append(types, m["role"].(string))
		}
	}
	if strings.Join(types, ",") != "developer,user,assistant,function_call,function_call_output" {
		t.Fatalf("items = %v (%s)", types, out)
	}
	if items[3].(map[string]any)["arguments"] != "{}" {
		t.Errorf("empty arguments should become {}")
	}
	if r["text"].(map[string]any)["format"].(map[string]any)["type"] != "json_object" {
		t.Errorf("text.format = %v", r["text"])
	}
	_, _, err = ChatToResponsesRequest([]byte(`{"model":"c","stop":"x","messages":[{"role":"user","content":"hi"}]}`), "g", Strict)
	if err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("stop must be unsupported: %v", err)
	}
}

func TestResponsesResponseConversions(t *testing.T) {
	resp := `{"id":"resp_9","object":"response","created_at":1,"model":"up","status":"completed","output":[
	  {"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"think"}]},
	  {"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
	  {"type":"function_call","id":"fc","call_id":"call_1","name":"f","arguments":"{\"x\":1}"}],
	  "usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":5},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":3}}}`
	chat, u, err := ResponsesToChatResponse([]byte(resp), "alias")
	if err != nil {
		t.Fatal(err)
	}
	if u != (Usage{Input: 15, CacheRead: 5, Output: 7, Reasoning: 3}) {
		t.Errorf("usage = %+v", u)
	}
	var c ChatResponse
	_ = json.Unmarshal(chat, &c)
	m := c.Choices[0].Message
	if *c.Choices[0].FinishReason != "tool_calls" || m.ReasoningContent != "think" || m.ToolCalls[0].ID != "call_1" || string(m.Content) != `"hello"` {
		t.Fatalf("chat = %s", chat)
	}
	back, u2, err := ChatToResponsesResponse(chat, "alias")
	if err != nil || u2 != u {
		t.Fatalf("back usage %+v err %v", u2, err)
	}
	var rr responsesResponse
	_ = json.Unmarshal(back, &rr)
	types := []string{}
	for _, it := range rr.Output {
		types = append(types, it.Type)
	}
	if strings.Join(types, ",") != "reasoning,message,function_call" || rr.Usage.InputTokens != 20 || rr.Object != "response" {
		t.Fatalf("responses = %s", back)
	}
	// Composed: Responses upstream -> Anthropic client.
	an, _, err := ConvertResponse(Anthropic, OpenAIResponses, []byte(resp), "alias")
	var a AnthropicResponse
	_ = json.Unmarshal(an, &a)
	if err != nil || *a.StopReason != "tool_use" || len(a.Content) != 3 || a.Content[0].Type != "thinking" {
		t.Fatalf("anthropic = %s %v", an, err)
	}
}

func eventsOf(t *testing.T, s string) []map[string]any {
	var out []map[string]any
	for _, ev := range readAll(t, s) {
		if string(ev.Data) == "[DONE]" {
			out = append(out, map[string]any{"type": "[DONE]"})
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(ev.Data, &m); err != nil {
			t.Fatalf("bad event %q", ev.Data)
		}
		out = append(out, m)
	}
	return out
}

const responsesStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"hmm"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"m1","delta":"Hi"}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_9","name":"f","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"a\":1}"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":4}}}

`

func TestResponsesStreamToChat(t *testing.T) {
	p, _ := NewStreamProcessor(OpenAIChat, OpenAIResponses, "m", true)
	evs := eventsOf(t, run(t, p, responsesStream))
	content, reasoning, args, finish := "", "", "", ""
	var usage map[string]any
	for _, e := range evs[:len(evs)-1] {
		if u, ok := e["usage"].(map[string]any); ok {
			usage = u
		}
		for _, ch := range e["choices"].([]any) {
			c := ch.(map[string]any)
			d := c["delta"].(map[string]any)
			if v, ok := d["content"].(string); ok {
				content += v
			}
			if v, ok := d["reasoning_content"].(string); ok {
				reasoning += v
			}
			if tcs, ok := d["tool_calls"].([]any); ok {
				args += tcs[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
			}
			if f, ok := c["finish_reason"].(string); ok {
				finish = f
			}
		}
	}
	if content != "Hi" || reasoning != "hmm" || args != `{"a":1}` || finish != "tool_calls" || usage["prompt_tokens"].(float64) != 10 || evs[len(evs)-1]["type"] != "[DONE]" {
		t.Fatalf("content=%q reasoning=%q args=%q finish=%q usage=%v", content, reasoning, args, finish, usage)
	}
}

func TestChatStreamToResponses(t *testing.T) {
	up := `data: {"id":"x","choices":[{"index":0,"delta":{"reasoning_content":"r"}}]}

data: {"id":"x","choices":[{"index":0,"delta":{"content":"He"}}]}

data: {"id":"x","choices":[{"index":0,"delta":{"content":"y"}}]}

data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}

data: {"id":"x","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":3}}

data: [DONE]

`
	p, _ := NewStreamProcessor(OpenAIResponses, OpenAIChat, "gpt", false)
	evs := eventsOf(t, run(t, p, up))
	var types []string
	for i, e := range evs {
		types = append(types, e["type"].(string))
		if int(e["sequence_number"].(float64)) != i {
			t.Fatalf("sequence_number %v at %d", e["sequence_number"], i)
		}
	}
	got := strings.Join(types, ",")
	for _, want := range []string{"response.created,response.in_progress,response.output_item.added,response.reasoning_summary_part.added",
		"response.output_text.delta,response.output_text.delta,response.output_text.done,response.content_part.done,response.output_item.done",
		"response.function_call_arguments.delta,response.function_call_arguments.done,response.output_item.done,response.completed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("event sequence missing %q:\n%s", want, got)
		}
	}
	final := evs[len(evs)-1]["response"].(map[string]any)
	if final["status"] != "completed" || len(final["output"].([]any)) != 3 || final["usage"].(map[string]any)["input_tokens"].(float64) != 8 {
		t.Fatalf("final = %v", final)
	}
}

func TestComposedStreams(t *testing.T) {
	// Anthropic client reading a Responses upstream.
	p, _ := NewStreamProcessor(Anthropic, OpenAIResponses, "claude-alias", false)
	out := run(t, p, responsesStream)
	if !p.Done() || !strings.Contains(out, `"stop_reason":"tool_use"`) || !strings.Contains(out, "thinking_delta") || !strings.Contains(out, "event: message_stop") {
		t.Fatalf("anthropic from responses:\n%s", out)
	}
	if u, ok := p.Usage(); !ok || u.Input != 10 || u.Output != 4 {
		t.Fatalf("usage = %+v %v", u, ok)
	}
	// Responses client reading an Anthropic upstream.
	up := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":6,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"yo"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
	p2, _ := NewStreamProcessor(OpenAIResponses, Anthropic, "gpt-alias", false)
	evs := eventsOf(t, run(t, p2, up))
	last := evs[len(evs)-1]
	if last["type"] != "response.completed" || !p2.Done() {
		t.Fatalf("last = %v", last)
	}
	resp := last["response"].(map[string]any)
	if resp["usage"].(map[string]any)["output_tokens"].(float64) != 2 || !strings.Contains(mustJSON(resp["output"]), `"yo"`) {
		t.Fatalf("response = %v", resp)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// prompt_cache_key and safety_identifier exist in both OpenAI formats: they
// survive Responses ⇄ Chat (upstream caches and account pools key sessions on
// them); service_tier stays a dropped hint, and Anthropic still drops them.
func TestSessionFieldsAcrossResponsesAndChat(t *testing.T) {
	out, warns, err := ResponsesToChatRequest([]byte(`{"model":"r","input":"hi","prompt_cache_key":"conv-1","safety_identifier":"u-1","service_tier":"flex"}`),
		"up", "max_tokens", Strict)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["prompt_cache_key"] != "conv-1" || m["safety_identifier"] != "u-1" || m["service_tier"] != nil || strings.Join(warns, ",") != "service_tier" {
		t.Fatalf("responses → chat: %s %v", out, warns)
	}
	out, warns, err = ChatToResponsesRequest([]byte(`{"model":"c","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"conv-2","safety_identifier":"u-2"}`),
		"up", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatal(err, warns)
	}
	m = nil
	_ = json.Unmarshal(out, &m)
	if m["prompt_cache_key"] != "conv-2" || m["safety_identifier"] != "u-2" {
		t.Fatalf("chat → responses: %s", out)
	}
	// Responses → Anthropic (through Chat) still drops them with a warning.
	out, warns, err = ConvertRequest(OpenAIResponses, Anthropic, []byte(`{"model":"r","input":"hi","prompt_cache_key":"conv-3"}`), "up", "max_tokens", Strict)
	if err != nil || strings.Contains(string(out), "conv-3") || strings.Join(warns, ",") != "prompt_cache_key" {
		t.Fatalf("responses → anthropic: %s %v %v", out, warns, err)
	}
}
