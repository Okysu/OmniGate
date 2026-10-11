package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	testPNG = "data:image/png;base64,iVBORw0KGgo="
	testPDF = "data:application/pdf;base64,JVBERi0xLjQK"
)

// chatToolHistory is a Chat request whose tool result carries an image, the
// shape agent clients (Zed, opencode, Codex-style view_image...) send.
const chatToolHistory = `{"model":"c","messages":[
  {"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":"` + testPNG + `"},
    {"type":"file","file":{"file_data":"` + testPDF + `","filename":"spec.pdf"}}]},
  {"role":"assistant","tool_calls":[{"id":"t1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
  {"role":"tool","tool_call_id":"t1","content":[{"type":"text","text":"cat.png"},{"type":"image_url","image_url":{"url":"` + testPNG + `","detail":"high"}}]}]}`

func TestChatMediaToResponses(t *testing.T) {
	out, warns, err := ConvertRequest(OpenAIChat, OpenAIResponses, []byte(chatToolHistory), "u", "", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", warns, err)
	}
	var r struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(out, &r)
	user := string(r.Input[0]["content"])
	for _, want := range []string{`"image_url":"` + testPNG + `","type":"input_image"`, `"type":"input_file"`, `"filename":"spec.pdf"`, `"file_data":"` + testPDF} {
		if !strings.Contains(user, want) {
			t.Errorf("user content missing %s: %s", want, user)
		}
	}
	output := string(r.Input[2]["output"])
	if !strings.Contains(output, `"text":"cat.png","type":"input_text"`) || !strings.Contains(output, `"detail":"high"`) ||
		!strings.Contains(output, `"type":"input_image"`) {
		t.Fatalf("function_call_output = %s", output)
	}
}

func TestChatMediaToAnthropic(t *testing.T) {
	out, warns, err := ConvertRequest(OpenAIChat, Anthropic, []byte(chatToolHistory), "u", "", Lenient)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(warns, ",") != "image_url.detail" {
		t.Errorf("warnings = %v", warns)
	}
	var r AnthropicRequest
	_ = json.Unmarshal(out, &r)
	user := string(r.Messages[0].Content)
	if !strings.Contains(user, `"type":"image"`) || !strings.Contains(user, `"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"},"title":"spec.pdf"`) {
		t.Fatalf("user = %s", user)
	}
	result := string(r.Messages[2].Content)
	if !strings.Contains(result, `"type":"tool_result"`) || !strings.Contains(result, `"content":[{"type":"text","text":"cat.png"},{"type":"image"`) {
		t.Fatalf("tool_result = %s", result)
	}
}

// Responses clients put media in function_call_output arrays.
const responsesToolHistory = `{"model":"r","input":[
  {"role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_file","filename":"spec.pdf","file_data":"` + testPDF + `"}]},
  {"type":"function_call","call_id":"c1","name":"shot","arguments":"{}"},
  {"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"done"},{"type":"input_image","image_url":"` + testPNG + `"}]},
  {"role":"user","content":"what is it?"}]}`

func TestResponsesMediaToChat(t *testing.T) {
	out, warns, err := ConvertRequest(OpenAIResponses, OpenAIChat, []byte(responsesToolHistory), "u", "max_tokens", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", warns, err)
	}
	var r ChatRequest
	_ = json.Unmarshal(out, &r)
	if len(r.Messages) != 4 {
		t.Fatalf("messages = %s", out)
	}
	if c := string(r.Messages[0].Content); !strings.Contains(c, `"type":"file","file":{"file_data":"`+testPDF+`","filename":"spec.pdf"}`) {
		t.Errorf("user = %s", c)
	}
	// A real Chat upstream takes text only in tool messages: the image moves
	// into the next user message, before the user's own text.
	if c := string(r.Messages[2].Content); c != `"done\n[image 1 attached below]"` {
		t.Errorf("tool = %s", c)
	}
	var parts []ChatPart
	_ = json.Unmarshal(r.Messages[3].Content, &parts)
	if len(parts) != 3 || parts[0].Text != "[Images returned by tool call c1]" || parts[1].ImageURL == nil || parts[2].Text != "what is it?" {
		t.Fatalf("user = %s", r.Messages[3].Content)
	}
}

func TestResponsesMediaToAnthropic(t *testing.T) {
	out, warns, err := ConvertRequest(OpenAIResponses, Anthropic, []byte(responsesToolHistory), "u", "", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", warns, err)
	}
	var r AnthropicRequest
	_ = json.Unmarshal(out, &r)
	if c := string(r.Messages[0].Content); !strings.Contains(c, `"type":"document"`) {
		t.Errorf("user = %s", c)
	}
	// tool_result and the following user text share one user turn.
	c := string(r.Messages[2].Content)
	if !strings.Contains(c, `"content":[{"type":"text","text":"done"},{"type":"image","source":{"type":"base64","media_type":"image/png"`) ||
		!strings.Contains(c, `"text":"what is it?"`) {
		t.Fatalf("tool turn = %s", c)
	}
}

func TestAnthropicMediaToResponses(t *testing.T) {
	body := `{"model":"c","max_tokens":10,"messages":[
	  {"role":"user","content":[{"type":"document","title":"spec.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"}},{"type":"text","text":"read"}]},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"shot","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}]}`
	out, warns, err := ConvertRequest(Anthropic, OpenAIResponses, []byte(body), "u", "", Strict)
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", warns, err)
	}
	s := string(out)
	if !strings.Contains(s, `"filename":"spec.pdf"`) ||
		!strings.Contains(s, `"output":[{"image_url":"`+testPNG+`","type":"input_image"}]`) {
		t.Fatalf("out = %s", s)
	}
}

func TestChatPartImageURLString(t *testing.T) {
	var p ChatPart
	if err := json.Unmarshal([]byte(`{"type":"image_url","image_url":"https://x/y.png"}`), &p); err != nil || p.ImageURL.URL != "https://x/y.png" {
		t.Fatalf("%+v %v", p, err)
	}
	if err := json.Unmarshal([]byte(`{"type":"image_url","image_url":{"url":"u","detail":"low"}}`), &p); err != nil || p.ImageURL.Detail != "low" {
		t.Fatalf("%+v %v", p, err)
	}
}
