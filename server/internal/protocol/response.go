package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func chatFinishToAnthropic(r string) string {
	switch r {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

func anthropicStopToChat(r string) string {
	switch r {
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}

// toolInput turns an OpenAI arguments string into an Anthropic input object.
func toolInput(args string) json.RawMessage {
	args = strings.TrimSpace(args)
	if args == "" {
		return json.RawMessage("{}")
	}
	var obj map[string]any
	if json.Unmarshal([]byte(args), &obj) == nil {
		return json.RawMessage(args)
	}
	b, _ := json.Marshal(map[string]string{"_raw_arguments": args})
	return b
}

// ChatToAnthropicResponse converts a non-streaming Chat response. model is the
// name the client asked for. Returns the body and normalized usage.
func ChatToAnthropicResponse(body []byte, model string) ([]byte, Usage, error) {
	var r ChatResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, Usage{}, fmt.Errorf("上游响应不是合法的 Chat Completions JSON: %w", err)
	}
	if len(r.Choices) == 0 || r.Choices[0].Message == nil {
		return nil, Usage{}, fmt.Errorf("上游响应缺少 choices")
	}
	msg := r.Choices[0].Message
	var blocks []AnthropicBlock
	if r := msg.reasoningText(); r != "" {
		blocks = append(blocks, AnthropicBlock{Type: "thinking", Thinking: r, Signature: ""})
	}
	if text, _ := chatContentText(msg.Content, &issues{}); text != "" {
		blocks = append(blocks, AnthropicBlock{Type: "text", Text: text})
	}
	for _, tc := range msg.ToolCalls {
		blocks = append(blocks, AnthropicBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: toolInput(tc.Function.Arguments)})
	}
	if blocks == nil {
		blocks = []AnthropicBlock{{Type: "text", Text: ""}}
	}
	finish := "stop"
	if r.Choices[0].FinishReason != nil {
		finish = *r.Choices[0].FinishReason
	}
	stop := chatFinishToAnthropic(finish)
	var u Usage
	if r.Usage != nil {
		u = r.Usage.normalize()
	}
	out := AnthropicResponse{
		ID: anthropicID(r.ID), Type: "message", Role: "assistant", Model: model,
		Content: blocks, StopReason: &stop, Usage: anthropicUsageFrom(u),
	}
	b, err := json.Marshal(out)
	return b, u, err
}

// AnthropicToChatResponse converts a non-streaming Messages response.
func AnthropicToChatResponse(body []byte, model string) ([]byte, Usage, error) {
	var r AnthropicResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, Usage{}, fmt.Errorf("上游响应不是合法的 Messages JSON: %w", err)
	}
	msg := ChatMessage{Role: "assistant"}
	var text, reasoning strings.Builder
	for _, bl := range r.Content {
		switch bl.Type {
		case "text":
			text.WriteString(bl.Text)
		case "thinking":
			reasoning.WriteString(bl.Thinking)
		case "tool_use":
			args := string(bl.Input)
			if isNull(bl.Input) {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{ID: bl.ID, Type: "function", Function: ChatToolCallFunc{Name: bl.Name, Arguments: args}})
		}
	}
	if text.Len() > 0 || len(msg.ToolCalls) == 0 {
		msg.Content, _ = json.Marshal(text.String())
	} else {
		msg.Content = json.RawMessage("null")
	}
	msg.ReasoningContent = reasoning.String()
	stop := ""
	if r.StopReason != nil {
		stop = *r.StopReason
	}
	finish := anthropicStopToChat(stop)
	u := r.Usage.normalize()
	out := ChatResponse{
		ID: chatID(r.ID), Object: "chat.completion", Created: time.Now().Unix(), Model: model,
		Choices: []ChatChoice{{Index: 0, Message: &msg, FinishReason: &finish}},
		Usage:   chatUsageFrom(u),
	}
	b, err := json.Marshal(out)
	return b, u, err
}

func anthropicID(id string) string {
	if strings.HasPrefix(id, "msg_") || id == "" {
		if id == "" {
			return "msg_" + randomID()
		}
		return id
	}
	return "msg_" + id
}

func chatID(id string) string {
	if id == "" {
		return "chatcmpl-" + randomID()
	}
	return "chatcmpl-" + strings.TrimPrefix(id, "msg_")
}
