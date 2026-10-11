package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// Structured outputs survive conversion in every direction (strict mode, so
// nothing is dropped silently).
func TestStructuredOutputConversion(t *testing.T) {
	schema := `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`

	// Messages → Chat: output_config.format → response_format, effort kept.
	anth := `{"model":"c","max_tokens":100,"messages":[{"role":"user","content":"hi"}],
	  "output_config":{"effort":"high","format":{"type":"json_schema","schema":` + schema + `}}}`
	out, warns, err := AnthropicToChatRequest([]byte(anth), "g", "max_tokens", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("anthropic → chat: %v %v", warns, err)
	}
	var chat map[string]any
	_ = json.Unmarshal(out, &chat)
	rf := chat["response_format"].(map[string]any)
	if rf["type"] != "json_schema" || rf["json_schema"].(map[string]any)["schema"].(map[string]any)["required"] == nil ||
		chat["reasoning_effort"] != "high" {
		t.Fatalf("chat = %s", out)
	}

	// Chat → Messages: response_format json_schema → output_config.format.
	chatReq := `{"model":"g","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":` + schema + `,"strict":true}}}`
	out, warns, err = ChatToAnthropicRequest([]byte(chatReq), "c", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("chat → anthropic: %v %v", warns, err)
	}
	var a map[string]any
	_ = json.Unmarshal(out, &a)
	f := a["output_config"].(map[string]any)["format"].(map[string]any)
	if f["type"] != "json_schema" || f["schema"].(map[string]any)["additionalProperties"] != false {
		t.Fatalf("anthropic = %s", out)
	}
	// text is a no-op, json_object has no Anthropic equivalent.
	if out, _, err := ChatToAnthropicRequest([]byte(`{"model":"g","messages":[{"role":"user","content":"x"}],"response_format":{"type":"text"}}`), "c", Strict); err != nil || strings.Contains(string(out), "output_config") {
		t.Fatalf("text format = %s %v", out, err)
	}
	if _, _, err := ChatToAnthropicRequest([]byte(`{"model":"g","messages":[{"role":"user","content":"x"}],"response_format":{"type":"json_object"}}`), "c", Strict); err == nil {
		t.Fatal("json_object must be reported in strict mode")
	}

	// Messages → Responses (through Chat): text.format json_schema.
	out, _, err = ConvertRequest(Anthropic, OpenAIResponses, []byte(anth), "g", "max_tokens", Strict)
	if err != nil || !strings.Contains(string(out), `"format":{`) || !strings.Contains(string(out), `"json_schema"`) {
		t.Fatalf("anthropic → responses = %s %v", out, err)
	}
	// Responses → Messages (through Chat): output_config.format.
	resp := `{"model":"g","input":"hi","text":{"format":{"type":"json_schema","name":"x","schema":` + schema + `,"strict":true}}}`
	out, _, err = ConvertRequest(OpenAIResponses, Anthropic, []byte(resp), "c", "max_tokens", Strict)
	if err != nil || !strings.Contains(string(out), `"output_config":{"format":{`) {
		t.Fatalf("responses → anthropic = %s %v", out, err)
	}
}

// Images in Anthropic tool results reach Chat upstreams (in the user message
// after the tool results), and stop sequences no longer fail a request to a
// Responses upstream.
func TestToolResultImagesAndStop(t *testing.T) {
	img := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}`
	body := `{"model":"c","max_tokens":10,"stop_sequences":["END"],"messages":[
	  {"role":"user","content":"look"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"screenshot","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"done"},` + img + `]}]}]}`
	out, warns, err := AnthropicToChatRequest([]byte(body), "g", "max_tokens", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("anthropic → chat: %v %v", warns, err)
	}
	var chat ChatRequest
	_ = json.Unmarshal(out, &chat)
	n := len(chat.Messages)
	tool, user := chat.Messages[n-2], chat.Messages[n-1]
	if tool.Role != "tool" || !strings.Contains(string(tool.Content), "[image 1 attached below]") ||
		user.Role != "user" || !strings.Contains(string(user.Content), "data:image/png;base64,iVBORw0KGgo=") {
		t.Fatalf("messages = %s", out)
	}
	out, warns, err = ConvertRequest(Anthropic, OpenAIResponses, []byte(body), "g", "max_tokens", Strict)
	if err != nil || !strings.Contains(strings.Join(warns, ","), "stop") || strings.Contains(string(out), `"stop"`) {
		t.Fatalf("anthropic → responses = %s %v %v", out, warns, err)
	}
}
