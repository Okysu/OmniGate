package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// Claude Code on a Responses upstream: the encrypted reasoning reaches the
// client in thinking.signature and goes back to the upstream next turn.
func TestReasoningAnthropicClientResponsesUpstream(t *testing.T) {
	up := `{"id":"resp_1","status":"completed","output":[
	  {"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"gAAAA-blob"},
	  {"type":"function_call","id":"fc_1","call_id":"call_1","name":"f","arguments":"{}"}],
	  "usage":{"input_tokens":5,"output_tokens":3}}`
	out, _, err := ConvertResponse(Anthropic, OpenAIResponses, []byte(up), "gpt")
	if err != nil {
		t.Fatal(err)
	}
	var r AnthropicResponse
	_ = json.Unmarshal(out, &r)
	if len(r.Content) != 2 || r.Content[0].Type != "thinking" || r.Content[0].Thinking != "plan" || !strings.HasPrefix(r.Content[0].Signature, "og1.") {
		t.Fatalf("content = %s", out)
	}
	sig := r.Content[0].Signature

	// The client sends the turn back.
	body := `{"model":"c","max_tokens":100,"messages":[
	  {"role":"user","content":"go"},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"` + sig + `"},{"type":"tool_use","id":"call_1","name":"f","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}]}`
	req, warns, err := ConvertRequest(Anthropic, OpenAIResponses, []byte(body), "gpt", "", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", warns, err)
	}
	var rr struct {
		Include []string         `json:"include"`
		Input   []map[string]any `json:"input"`
	}
	_ = json.Unmarshal(req, &rr)
	if strings.Join(rr.Include, ",") != "reasoning.encrypted_content" {
		t.Errorf("include = %v", rr.Include)
	}
	if len(rr.Input) != 4 || rr.Input[1]["type"] != "reasoning" || rr.Input[1]["id"] != "rs_1" || rr.Input[1]["encrypted_content"] != "gAAAA-blob" ||
		rr.Input[2]["type"] != "function_call" {
		t.Fatalf("input = %s", req)
	}
	if strings.Contains(string(req), "x_og_") {
		t.Errorf("private fields leaked: %s", req)
	}
}

func TestReasoningAnthropicClientResponsesStream(t *testing.T) {
	up := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\"}}\n\n" +
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_1\",\"delta\":\"plan\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"gAAAA-blob\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_2\"}}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_2\",\"encrypted_content\":\"gAAAA-two\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	p, _ := NewStreamProcessor(Anthropic, OpenAIResponses, "gpt", false)
	out := run(t, p, up)
	var sigs []string
	var starts []string
	for _, ev := range readAll(t, out) {
		var e anthropicStreamEvent
		_ = json.Unmarshal(ev.Data, &e)
		if e.Type == "content_block_start" {
			starts = append(starts, e.ContentBlock.Type)
		}
		if e.Type == "content_block_delta" && e.Delta.Type == "signature_delta" {
			sigs = append(sigs, e.Delta.Signature)
		}
	}
	if strings.Join(starts, ",") != "thinking,thinking,text" || len(sigs) != 2 {
		t.Fatalf("blocks = %v sigs = %v\n%s", starts, sigs, out)
	}
	for i, want := range []string{"gAAAA-blob", "gAAAA-two"} {
		b, ok := openEnvelope(sigs[i], "")
		if !ok || b.Provider != ReasoningOpenAI || b.Blob != want {
			t.Errorf("sig %d = %+v", i, b)
		}
	}
	if strings.Contains(out, "x_og_") {
		t.Errorf("private fields leaked: %s", out)
	}
}

// Codex-style Responses client on an Anthropic upstream: the thinking
// signature travels in encrypted_content and goes back as a thinking block.
func TestReasoningResponsesClientAnthropicUpstream(t *testing.T) {
	up := `{"id":"msg_1","type":"message","role":"assistant","content":[
	  {"type":"thinking","thinking":"plan","signature":"sig-1"},
	  {"type":"redacted_thinking","data":"secret"},
	  {"type":"tool_use","id":"toolu_1","name":"f","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`
	out, _, err := ConvertResponse(OpenAIResponses, Anthropic, []byte(up), "claude")
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Output []map[string]any `json:"output"`
	}
	_ = json.Unmarshal(out, &r)
	if len(r.Output) != 3 || r.Output[0]["type"] != "reasoning" || r.Output[1]["type"] != "reasoning" {
		t.Fatalf("output = %s", out)
	}
	enc1, enc2 := r.Output[0]["encrypted_content"].(string), r.Output[1]["encrypted_content"].(string)

	body := `{"model":"r","input":[{"role":"user","content":"go"},
	  {"type":"reasoning","id":"rs_x","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"` + enc1 + `"},
	  {"type":"reasoning","id":"rs_y","summary":[],"encrypted_content":"` + enc2 + `"},
	  {"type":"function_call","call_id":"toolu_1","name":"f","arguments":"{}"},
	  {"type":"function_call_output","call_id":"toolu_1","output":"ok"}]}`
	req, warns, err := ConvertRequest(OpenAIResponses, Anthropic, []byte(body), "claude", "", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", warns, err)
	}
	var ar AnthropicRequest
	_ = json.Unmarshal(req, &ar)
	var blocks []AnthropicBlock
	_ = json.Unmarshal(ar.Messages[1].Content, &blocks)
	if len(blocks) != 3 || blocks[0].Type != "thinking" || blocks[0].Signature != "sig-1" || blocks[0].Thinking != "plan" ||
		blocks[1].Type != "redacted_thinking" || blocks[1].Data != "secret" || blocks[2].Type != "tool_use" {
		t.Fatalf("assistant = %s", ar.Messages[1].Content)
	}
}

func TestReasoningResponsesClientAnthropicStream(t *testing.T) {
	up := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig-\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"1\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	p, _ := NewStreamProcessor(OpenAIResponses, Anthropic, "claude", false)
	out := run(t, p, up)
	var final map[string]any
	for _, ev := range readAll(t, out) {
		var e struct {
			Type     string         `json:"type"`
			Response map[string]any `json:"response"`
		}
		_ = json.Unmarshal(ev.Data, &e)
		if e.Type == "response.completed" {
			final = e.Response
		}
	}
	items, _ := final["output"].([]any)
	if len(items) != 2 {
		t.Fatalf("output = %v\n%s", final, out)
	}
	rs := items[0].(map[string]any)
	b, ok := openEnvelope(rs["encrypted_content"].(string), "")
	if rs["type"] != "reasoning" || !ok || b.Provider != ReasoningAnthropic || b.Blob != "sig-1" {
		t.Fatalf("reasoning = %v", rs)
	}
	if summary := rs["summary"].([]any); len(summary) != 1 || summary[0].(map[string]any)["text"] != "plan" {
		t.Errorf("summary = %v", rs["summary"])
	}
}

// Reasoning only goes to the provider that produced it; Chat upstreams get
// the text on tool-call turns.
func TestReasoningCrossProvider(t *testing.T) {
	anth := ReasoningBlock{Provider: ReasoningAnthropic, Blob: "sig-1"}.envelope()
	body := `{"model":"c","max_tokens":100,"messages":[
	  {"role":"user","content":"go"},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"` + anth + `"},{"type":"tool_use","id":"t1","name":"f","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"done","signature":"` + anth + `"},{"type":"text","text":"bye"}]},
	  {"role":"user","content":"again"}]}`
	req, warns, err := ConvertRequest(Anthropic, OpenAIResponses, []byte(body), "gpt", "", Strict)
	if err != nil || strings.Join(warns, ",") != "messages[].reasoning_content" || strings.Contains(string(req), `"reasoning"`) {
		t.Fatalf("anthropic blob to responses: %v %v %s", warns, err, req)
	}
	req, warns, err = ConvertRequest(Anthropic, OpenAIChat, []byte(body), "glm", "max_tokens", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", warns, err)
	}
	var cr ChatRequest
	_ = json.Unmarshal(req, &cr)
	if cr.Messages[1].ReasoningContent != "plan" || cr.Messages[3].ReasoningContent != "" || strings.Contains(string(req), "x_og_") {
		t.Fatalf("chat = %s", req)
	}
	// Chat clients never see the private fields.
	up := `{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"gAAAA"},
	  {"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`
	out, _, _ := ConvertResponse(OpenAIChat, OpenAIResponses, []byte(up), "gpt")
	if strings.Contains(string(out), "x_og_") || strings.Contains(string(out), "gAAAA") || !strings.Contains(string(out), `"reasoning_content":"plan"`) {
		t.Fatalf("chat response = %s", out)
	}
}

// A converted conversation that continues on a native channel gets the
// provider's own blobs back and loses the rest.
func TestUnwrapReasoningForPassthrough(t *testing.T) {
	anth := ReasoningBlock{Provider: ReasoningAnthropic, Blob: "sig-1"}.envelope()
	oai := ReasoningBlock{Provider: ReasoningOpenAI, ID: "rs_1", Blob: "gAAAA"}.envelope()
	body := `{"model":"c","messages":[{"role":"assistant","content":[
	  {"type":"thinking","thinking":"a","signature":"` + anth + `"},
	  {"type":"thinking","thinking":"b","signature":"` + oai + `"},
	  {"type":"thinking","thinking":"c","signature":"native"},
	  {"type":"text","text":"x"}]}]}`
	out, err := RewriteForPassthrough(Anthropic, []byte(body), "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	var r AnthropicRequest
	_ = json.Unmarshal(out, &r)
	var blocks []AnthropicBlock
	_ = json.Unmarshal(r.Messages[0].Content, &blocks)
	if len(blocks) != 3 || blocks[0].Signature != "sig-1" || blocks[1].Signature != "native" || blocks[2].Type != "text" {
		t.Fatalf("anthropic = %s", r.Messages[0].Content)
	}
	rbody := `{"model":"r","input":[{"type":"reasoning","id":"rs_z","summary":[],"encrypted_content":"` + oai + `"},
	  {"type":"reasoning","id":"rs_q","summary":[],"encrypted_content":"` + anth + `"},{"role":"user","content":"x"}]}`
	out, _ = RewriteForPassthrough(OpenAIResponses, []byte(rbody), "gpt", false)
	if s := string(out); strings.Count(s, `"type":"reasoning"`) != 1 || !strings.Contains(s, `"encrypted_content":"gAAAA"`) || !strings.Contains(s, `"id":"rs_1"`) {
		t.Fatalf("responses = %s", s)
	}
}
