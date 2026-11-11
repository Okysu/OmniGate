package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeepSeekClaudeCodeReasoningReplay(t *testing.T) {
	response := []byte(`{"id":"r","status":"completed","output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"plan"}]},{"type":"function_call","call_id":"call1","name":"read","arguments":"{}"}]}`)
	out, _, err := ConvertResponse(Anthropic, OpenAIResponses, response, "deepseek-v4.1-flash")
	if err != nil {
		t.Fatal(err)
	}
	var a AnthropicResponse
	_ = json.Unmarshal(out, &a)
	if len(a.Content) != 2 || a.Content[0].Thinking != "plan" {
		t.Fatalf("reasoning lost: %s", out)
	}
	blocks, _ := json.Marshal(a.Content)
	body := []byte(`{"model":"d","max_tokens":200,"thinking":{"type":"adaptive"},"system":"sys","messages":[{"role":"user","content":"go"},{"role":"assistant","content":` + string(blocks) + `},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call1","content":"ok"}]}]}`)
	req, w, err := ConvertRequest(Anthropic, OpenAIResponses, body, "deepseek-v4.1-flash", "", Strict)
	if err != nil || len(w) != 0 {
		t.Fatalf("%s %v %v", req, w, err)
	}
	var r struct {
		Reasoning map[string]string
		Input     []map[string]any
	}
	_ = json.Unmarshal(req, &r)
	if r.Reasoning["effort"] != "high" || r.Input[0]["role"] != "system" || r.Input[2]["content"] != "plan" {
		t.Fatalf("bad replay %s", req)
	}
	if strings.Contains(string(req), "encrypted_content") {
		t.Fatalf("foreign encrypted reasoning: %s", req)
	}
}

func TestGPTImageGuidanceCompatibility(t *testing.T) {
	for _, model := range []string{"gpt-image-2.6-flare", "other-image"} {
		out, err := RewriteForPassthrough(OpenAIImagesGenerations, []byte(`{"model":"m","guidance_scale":7,"output_format":"gif","frame_count":9,"frame_duration_ms":200}`), model, false)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		_ = json.Unmarshal(out, &obj)
		_, has := obj["guidance_scale"]
		if has == strings.HasPrefix(model, "gpt-image-") || obj["frame_count"] != float64(9) || obj["output_format"] != "gif" {
			t.Fatalf("bad fields %s", out)
		}
	}
}

func TestClaudeCodeHintsDoNotRejectStrictConversion(t *testing.T) {
	body := []byte(`{"model":"d","max_tokens":200,"cache_control":{"type":"ephemeral"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"user","content":"hi"}]}`)
	for _, compat := range []Compat{Strict, Lenient} {
		out, w, err := ConvertRequest(Anthropic, OpenAIResponses, body, "deepseek-v4.1-flash", "", compat)
		if err != nil || strings.Join(w, ",") != "cache_control,context_management" {
			t.Fatalf("%s %v %v", out, w, err)
		}
		if strings.Contains(string(out), "cache_control") || strings.Contains(string(out), "context_management") {
			t.Fatalf("unsupported hints leaked %s", out)
		}
	}
	native, err := RewriteForPassthrough(Anthropic, body, "claude-sonnet", false)
	if err != nil || !strings.Contains(string(native), "cache_control") || !strings.Contains(string(native), "context_management") {
		t.Fatalf("native hints lost %s %v", native, err)
	}
}
