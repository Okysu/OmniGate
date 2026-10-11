package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// OpenAI Responses API <-> Chat Completions conversion. Responses <-> Anthropic
// Messages is composed through Chat (see convert.go).

type responsesRequest struct {
	Model             string          `json:"model"`
	Input             json.RawMessage `json:"input"`
	Instructions      string          `json:"instructions,omitempty"`
	MaxOutputTokens   *int64          `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Tools             []responsesTool `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Stream            bool            `json:"stream,omitempty"`
	Reasoning         *struct {
		Effort  string `json:"effort,omitempty"`
		Summary string `json:"summary,omitempty"`
	} `json:"reasoning,omitempty"`
	Text *struct {
		Format json.RawMessage `json:"format,omitempty"`
	} `json:"text,omitempty"`
	User             string `json:"user,omitempty"`
	Store            *bool  `json:"store,omitempty"`
	PromptCacheKey   string `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier string `json:"safety_identifier,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type responsesItem struct {
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	ID        string          `json:"id,omitempty"`
	Summary   json.RawMessage `json:"summary,omitempty"`
	// reasoning
	EncryptedContent string `json:"encrypted_content,omitempty"`
}

type responsesPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// prompt_cache_key and safety_identifier mean the same in Chat and Responses
// and are carried over; service_tier stays a dropped hint (it selects a
// processing / billing tier that OpenAI-compatible Chat upstreams rarely
// implement, so it is only kept by same-protocol passthrough).
var responsesToChatPolicy = fieldPolicy{
	handled: set("model", "input", "instructions", "max_output_tokens", "temperature", "top_p", "tools", "tool_choice",
		"parallel_tool_calls", "stream", "reasoning", "text", "user", "prompt_cache_key", "safety_identifier"),
	neutral: map[string]func(json.RawMessage) bool{
		"store": func(json.RawMessage) bool { return true }, "metadata": emptyJSON, "truncation": equalsJSON(`"disabled"`),
		"background": equalsJSON("false"), "include": emptyJSON,
	},
	hints: set("service_tier", "max_tool_calls"),
}

var chatToResponsesPolicy = fieldPolicy{
	handled: set("model", "messages", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stream", "stream_options",
		"tools", "tool_choice", "parallel_tool_calls", "user", "reasoning_effort", "response_format", "prompt_cache_key", "safety_identifier",
		"x_og_keep_reasoning"),
	neutral: map[string]func(json.RawMessage) bool{
		"n": equalsJSON("1"), "presence_penalty": equalsJSON("0"), "frequency_penalty": equalsJSON("0"),
		"logprobs": equalsJSON("false"), "store": equalsJSON("false"), "metadata": emptyJSON,
	},
	// The Responses API has no stop sequences: dropped with a warning (agent
	// clients such as Claude Code send them; failing the request is worse).
	hints: set("service_tier", "stop"),
}

// ResponsesToChatRequest converts a Responses request into a Chat request.
func ResponsesToChatRequest(body []byte, upstreamModel, maxTokensField string, compat Compat) ([]byte, []string, error) {
	return toChatUpstream(responsesToChatPivot(body, upstreamModel, maxTokensField, compat))
}

// responsesToChatPivot is ResponsesToChatRequest leaving function_call_output
// media on the tool messages, for composing with another converter.
func responsesToChatPivot(body []byte, upstreamModel, maxTokensField string, compat Compat) ([]byte, []string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, invalid("请求体不是合法的 JSON：%v", err)
	}
	var is issues
	responsesToChatPolicy.check(raw, &is)
	if v, ok := raw["previous_response_id"]; ok && !isNull(v) {
		return nil, nil, &ConvertError{Class: "unsupported_parameter", Message: "previous_response_id 依赖上游保存的会话状态，无法转换为其他协议；请在请求中携带完整的 input", Fields: []string{"previous_response_id"}}
	}
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, invalid("请求格式错误：%v", err)
	}
	out := ChatRequest{Model: upstreamModel, Stream: req.Stream, Temperature: req.Temperature, TopP: req.TopP,
		ParallelToolCalls: req.ParallelToolCalls, User: req.User, PromptCacheKey: req.PromptCacheKey, SafetyIdentifier: req.SafetyIdentifier}
	if req.Stream {
		out.StreamOptions = &StreamOptions{IncludeUsage: true}
	}
	if req.MaxOutputTokens != nil {
		mt := *req.MaxOutputTokens
		if maxTokensField == "max_completion_tokens" {
			out.MaxCompletionTokens = &mt
		} else {
			out.MaxTokens = &mt
		}
	}
	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		out.ReasoningEffort = req.Reasoning.Effort
		if req.Reasoning.Summary != "" {
			is.drop("reasoning.summary")
		}
	}
	if req.Instructions != "" {
		out.Messages = append(out.Messages, chatText("system", req.Instructions))
	}

	var s string
	if json.Unmarshal(req.Input, &s) == nil {
		out.Messages = append(out.Messages, chatText("user", s))
	} else {
		var items []responsesItem
		if err := json.Unmarshal(req.Input, &items); err != nil {
			return nil, nil, invalid("input 必须是字符串或输入项数组")
		}
		// Reasoning items belong to the assistant output that follows them.
		var reasoning []ReasoningBlock
		attach := func(m *ChatMessage) {
			if len(reasoning) == 0 {
				return
			}
			m.OGReasoning = append(m.OGReasoning, reasoning...)
			m.ReasoningContent = reasoningBlocksText(m.OGReasoning)
			reasoning = nil
		}
		for i, it := range items {
			switch {
			case it.Type == "function_call":
				msg := ChatMessage{Role: "assistant", ToolCalls: []ChatToolCall{{ID: it.CallID, Type: "function", Function: ChatToolCallFunc{Name: it.Name, Arguments: it.Arguments}}}}
				// Merge consecutive calls into one assistant message (parallel tool calls).
				if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == "assistant" && len(out.Messages[n-1].ToolCalls) > 0 {
					out.Messages[n-1].ToolCalls = append(out.Messages[n-1].ToolCalls, msg.ToolCalls...)
					attach(&out.Messages[n-1])
				} else {
					attach(&msg)
					out.Messages = append(out.Messages, msg)
				}
			case it.Type == "function_call_output":
				// output is a string or an array of input_text / input_image /
				// input_file parts; media stays on the tool message in the pivot.
				var c json.RawMessage
				var text string
				var parts []responsesPart
				switch {
				case json.Unmarshal(it.Output, &text) == nil:
					c, _ = json.Marshal(text)
				case json.Unmarshal(it.Output, &parts) == nil:
					var cp []ChatPart
					for _, p := range parts {
						if q := responsesPartToChat(p, &is); q != nil {
							cp = append(cp, *q)
						}
					}
					if hasMedia(cp) {
						c, _ = json.Marshal(cp)
					} else {
						var b strings.Builder
						for _, q := range cp {
							b.WriteString(q.Text)
						}
						c, _ = json.Marshal(b.String())
					}
				default:
					c, _ = json.Marshal(string(it.Output))
				}
				out.Messages = append(out.Messages, ChatMessage{Role: "tool", ToolCallID: it.CallID, Content: c})
			case it.Type == "reasoning":
				b, ok := openEnvelope(it.EncryptedContent, ReasoningOpenAI)
				if !ok {
					b = ReasoningBlock{Provider: ReasoningText}
				}
				if b.ID == "" && b.Provider == ReasoningOpenAI {
					b.ID = it.ID
				}
				b.Text = summaryText(it.Summary)
				reasoning = append(reasoning, b)
			case it.Type == "message" || it.Type == "" && it.Role != "":
				msg, err := responsesMessageToChat(it, &is)
				if err != nil {
					return nil, nil, invalid("input[%d]：%v", i, err)
				}
				if msg.Role == "assistant" {
					attach(&msg)
				}
				out.Messages = append(out.Messages, msg)
			default:
				is.unsupportedField("input[].type=" + it.Type)
			}
		}
		if len(reasoning) > 0 {
			is.drop("input[].reasoning")
		}
	}
	if len(out.Messages) == 0 {
		return nil, nil, invalid("input 不能为空")
	}
	for _, t := range req.Tools {
		if t.Type != "function" {
			is.unsupportedField("tools[].type=" + t.Type)
			continue
		}
		out.Tools = append(out.Tools, ChatTool{Type: "function", Function: ChatFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters, Strict: t.Strict}})
	}
	if len(req.ToolChoice) > 0 && !isNull(req.ToolChoice) {
		var str string
		var obj struct{ Type, Name string }
		switch {
		case json.Unmarshal(req.ToolChoice, &str) == nil:
			out.ToolChoice = req.ToolChoice
		case json.Unmarshal(req.ToolChoice, &obj) == nil && obj.Type == "function":
			out.ToolChoice, _ = json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": obj.Name}})
		default:
			is.unsupportedField("tool_choice")
		}
	}
	if req.Text != nil && len(req.Text.Format) > 0 && !isNull(req.Text.Format) {
		var f map[string]json.RawMessage
		_ = json.Unmarshal(req.Text.Format, &f)
		var typ string
		_ = json.Unmarshal(f["type"], &typ)
		switch typ {
		case "text":
		case "json_object":
			raw["response_format"] = json.RawMessage(`{"type":"json_object"}`)
		case "json_schema":
			js := map[string]json.RawMessage{}
			for _, k := range []string{"name", "schema", "strict", "description"} {
				if v, ok := f[k]; ok {
					js[k] = v
				}
			}
			b, _ := json.Marshal(map[string]any{"type": "json_schema", "json_schema": js})
			raw["response_format"] = b
		default:
			is.unsupportedField("text.format")
		}
	}
	warnings, err := is.resolve(compat, OpenAIChat)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	if rf, ok := raw["response_format"]; ok {
		b = setJSONField(b, "response_format", rf)
	}
	return b, warnings, nil
}

func chatPartsToResponses(parts []ChatPart, is *issues) ([]map[string]any, error) {
	var out []map[string]any
	for _, p := range parts {
		c, err := chatPartToResponses(p, is)
		if err != nil {
			return nil, err
		}
		if c != nil {
			out = append(out, c)
		}
	}
	return out, nil
}

func setJSONField(b []byte, key string, v json.RawMessage) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	m[key] = v
	out, _ := json.Marshal(m)
	return out
}

func responsesMessageToChat(it responsesItem, is *issues) (ChatMessage, error) {
	role := it.Role
	if role == "developer" {
		role = "system"
	}
	if role != "user" && role != "assistant" && role != "system" {
		return ChatMessage{}, fmt.Errorf("不支持的角色 %q", it.Role)
	}
	var s string
	if json.Unmarshal(it.Content, &s) == nil {
		return chatText(role, s), nil
	}
	var parts []responsesPart
	if err := json.Unmarshal(it.Content, &parts); err != nil {
		return ChatMessage{}, fmt.Errorf("content 格式错误")
	}
	var cp []ChatPart
	var text strings.Builder
	for _, p := range parts {
		if p.Type == "refusal" {
			text.WriteString(p.Text)
			continue
		}
		if role != "user" && p.Type != "input_text" && p.Type != "output_text" && p.Type != "text" {
			is.unsupportedField("content[].type=" + p.Type)
			continue
		}
		if q := responsesPartToChat(p, is); q != nil {
			cp = append(cp, *q)
			text.WriteString(q.Text)
		}
	}
	if role != "user" { // assistant/system content must be text
		return chatText(role, text.String()), nil
	}
	return chatParts(role, cp), nil
}

// ChatToResponsesRequest converts a Chat request into a Responses request.
func ChatToResponsesRequest(body []byte, upstreamModel string, compat Compat) ([]byte, []string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, invalid("请求体不是合法的 JSON：%v", err)
	}
	var is issues
	chatToResponsesPolicy.check(raw, &is)
	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, invalid("请求格式错误：%v", err)
	}
	out := map[string]any{"model": upstreamModel, "stream": req.Stream, "store": false}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	switch {
	case req.MaxCompletionTokens != nil:
		out["max_output_tokens"] = *req.MaxCompletionTokens
	case req.MaxTokens != nil:
		out["max_output_tokens"] = *req.MaxTokens
	}
	if req.ReasoningEffort != "" {
		out["reasoning"] = map[string]string{"effort": req.ReasoningEffort, "summary": "auto"}
	}
	if req.OGKeepReasoning {
		// The client keeps reasoning blobs (reasoning.go): with store=false the
		// encrypted reasoning is the only way the model sees it again.
		out["include"] = []string{"reasoning.encrypted_content"}
	}
	if req.ParallelToolCalls != nil {
		out["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.User != "" {
		out["user"] = req.User
	}
	if req.PromptCacheKey != "" {
		out["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.SafetyIdentifier != "" {
		out["safety_identifier"] = req.SafetyIdentifier
	}
	var items []any
	for i, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			text, err := chatContentText(m.Content, &is)
			if err != nil {
				return nil, nil, invalid("messages[%d]：%v", i, err)
			}
			items = append(items, map[string]any{"role": "developer", "content": text})
		case "user":
			var s string
			if json.Unmarshal(m.Content, &s) == nil {
				items = append(items, map[string]any{"role": "user", "content": s})
				continue
			}
			var parts []ChatPart
			if err := json.Unmarshal(m.Content, &parts); err != nil {
				return nil, nil, invalid("messages[%d]：content 格式错误", i)
			}
			content, err := chatPartsToResponses(parts, &is)
			if err != nil {
				return nil, nil, invalid("messages[%d]：%v", i, err)
			}
			items = append(items, map[string]any{"role": "user", "content": content})
		case "assistant":
			// Only OpenAI encrypted reasoning can go back as reasoning items; the
			// items precede the message and calls they produced.
			kept := 0
			for _, b := range m.OGReasoning {
				if b.Provider != ReasoningOpenAI || b.Blob == "" {
					continue
				}
				summary := []map[string]string{}
				if b.Text != "" {
					summary = append(summary, map[string]string{"type": "summary_text", "text": b.Text})
				}
				item := map[string]any{"type": "reasoning", "summary": summary, "encrypted_content": b.Blob}
				if b.ID != "" {
					item["id"] = b.ID
				}
				items = append(items, item)
				kept++
			}
			if kept < len(m.OGReasoning) || len(m.OGReasoning) == 0 && m.reasoningText() != "" {
				is.drop("messages[].reasoning_content")
			}
			text, err := chatContentText(m.Content, &is)
			if err != nil {
				return nil, nil, invalid("messages[%d]：%v", i, err)
			}
			if text != "" {
				items = append(items, map[string]any{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": text}}})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Function.Arguments
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				items = append(items, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Function.Name, "arguments": args})
			}
		case "tool":
			// function_call_output takes a string, or an array of text / image /
			// file parts when the tool returned media (screenshots, read_file on
			// an image...).
			parts, err := chatContentParts(m.Content)
			if err != nil {
				return nil, nil, invalid("messages[%d]：%v", i, err)
			}
			var output any
			if hasMedia(parts) {
				if output, err = chatPartsToResponses(parts, &is); err != nil {
					return nil, nil, invalid("messages[%d]：%v", i, err)
				}
			} else if output, err = chatContentText(m.Content, &is); err != nil {
				return nil, nil, invalid("messages[%d]：%v", i, err)
			}
			items = append(items, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": output})
		default:
			return nil, nil, invalid("不支持的消息角色 %q", m.Role)
		}
	}
	if len(items) == 0 {
		return nil, nil, invalid("messages 不能为空")
	}
	out["input"] = items
	var tools []map[string]any
	for _, t := range req.Tools {
		if t.Type != "function" {
			is.unsupportedField("tools[].type=" + t.Type)
			continue
		}
		tool := map[string]any{"type": "function", "name": t.Function.Name, "parameters": t.Function.Parameters}
		if isNull(t.Function.Parameters) {
			tool["parameters"] = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if t.Function.Description != "" {
			tool["description"] = t.Function.Description
		}
		if t.Function.Strict != nil {
			tool["strict"] = *t.Function.Strict
		}
		tools = append(tools, tool)
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if len(req.ToolChoice) > 0 && !isNull(req.ToolChoice) {
		var str string
		var obj struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		switch {
		case json.Unmarshal(req.ToolChoice, &str) == nil:
			out["tool_choice"] = str
		case json.Unmarshal(req.ToolChoice, &obj) == nil && obj.Type == "function":
			out["tool_choice"] = map[string]string{"type": "function", "name": obj.Function.Name}
		default:
			is.unsupportedField("tool_choice")
		}
	}
	if rf, ok := raw["response_format"]; ok && !isNull(rf) {
		var f struct {
			Type       string                     `json:"type"`
			JSONSchema map[string]json.RawMessage `json:"json_schema"`
		}
		_ = json.Unmarshal(rf, &f)
		switch f.Type {
		case "text":
		case "json_object":
			out["text"] = map[string]any{"format": map[string]string{"type": "json_object"}}
		case "json_schema":
			format := map[string]json.RawMessage{"type": json.RawMessage(`"json_schema"`)}
			for k, v := range f.JSONSchema {
				format[k] = v
			}
			out["text"] = map[string]any{"format": format}
		default:
			is.unsupportedField("response_format")
		}
	}
	warnings, err := is.resolve(compat, OpenAIResponses)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(out)
	return b, warnings, err
}

// ---- non-streaming responses ----

type responsesResponse struct {
	ID                string          `json:"id"`
	Object            string          `json:"object"`
	CreatedAt         int64           `json:"created_at"`
	Model             string          `json:"model"`
	Status            string          `json:"status"`
	Output            []responsesItem `json:"output"`
	Usage             *responsesUsage `json:"usage,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Error json.RawMessage `json:"error,omitempty"`
}

func summaryText(raw json.RawMessage) string {
	var parts []responsesPart
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// ResponsesToChatResponse converts a Responses response into a Chat response.
func ResponsesToChatResponse(body []byte, model string) ([]byte, Usage, error) {
	return responsesToChatResponse(body, model, false)
}

// responsesToChatResponse with pivot set keeps the reasoning items'
// encrypted_content in x_og_reasoning for the next converter.
func responsesToChatResponse(body []byte, model string, pivot bool) ([]byte, Usage, error) {
	var r responsesResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, Usage{}, fmt.Errorf("上游响应不是合法的 Responses JSON: %w", err)
	}
	msg := ChatMessage{Role: "assistant"}
	var text, reasoning strings.Builder
	for _, it := range r.Output {
		switch it.Type {
		case "message":
			var parts []responsesPart
			_ = json.Unmarshal(it.Content, &parts)
			for _, p := range parts {
				if p.Type == "output_text" {
					text.WriteString(p.Text)
				}
			}
		case "reasoning":
			t := summaryText(it.Summary)
			if reasoning.Len() > 0 && t != "" {
				reasoning.WriteString("\n\n")
			}
			reasoning.WriteString(t)
			if pivot {
				msg.OGReasoning = append(msg.OGReasoning, ReasoningBlock{Provider: ReasoningOpenAI, ID: it.ID, Text: t, Blob: it.EncryptedContent})
			}
		case "function_call":
			msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{ID: it.CallID, Type: "function", Function: ChatToolCallFunc{Name: it.Name, Arguments: it.Arguments}})
		}
	}
	if text.Len() > 0 || len(msg.ToolCalls) == 0 {
		msg.Content, _ = json.Marshal(text.String())
	} else {
		msg.Content = json.RawMessage("null")
	}
	msg.ReasoningContent = reasoning.String()
	finish := "stop"
	switch {
	case len(msg.ToolCalls) > 0:
		finish = "tool_calls"
	case r.Status == "incomplete" && r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "max_output_tokens":
		finish = "length"
	case r.Status == "incomplete" && r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "content_filter":
		finish = "content_filter"
	}
	var u Usage
	if r.Usage != nil {
		u = r.Usage.normalize()
	}
	out := ChatResponse{ID: chatID(strings.TrimPrefix(r.ID, "resp_")), Object: "chat.completion", Created: time.Now().Unix(), Model: model,
		Choices: []ChatChoice{{Index: 0, Message: &msg, FinishReason: &finish}}, Usage: chatUsageFrom(u)}
	b, err := json.Marshal(out)
	return b, u, err
}

func responsesUsageFrom(u Usage) map[string]any {
	return map[string]any{
		"input_tokens":          u.Input + u.CacheRead + u.CacheWrite,
		"input_tokens_details":  map[string]int64{"cached_tokens": u.CacheRead},
		"output_tokens":         u.Output,
		"output_tokens_details": map[string]int64{"reasoning_tokens": u.Reasoning},
		"total_tokens":          u.Input + u.CacheRead + u.CacheWrite + u.Output,
	}
}

func respID() string { return "resp_" + randomID() }

// responsesReasoningItem renders a reasoning block for a Responses client: a
// provider blob travels in encrypted_content.
func responsesReasoningItem(id string, b ReasoningBlock) map[string]any {
	summary := []map[string]string{}
	if b.Text != "" {
		summary = append(summary, map[string]string{"type": "summary_text", "text": b.Text})
	}
	item := map[string]any{"type": "reasoning", "id": id, "summary": summary}
	if b.Blob != "" {
		item["encrypted_content"] = b.envelope()
	}
	return item
}

// chatOutputItems builds Responses output items from chat content.
func chatOutputItems(reasoning []ReasoningBlock, text string, calls []ChatToolCall) []map[string]any {
	var out []map[string]any
	for _, b := range reasoning {
		out = append(out, responsesReasoningItem("rs_"+randomID(), b))
	}
	if text != "" || len(calls) == 0 {
		out = append(out, map[string]any{"type": "message", "id": "msg_" + randomID(), "role": "assistant", "status": "completed",
			"content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}}})
	}
	for _, c := range calls {
		args := c.Function.Arguments
		if args == "" {
			args = "{}"
		}
		out = append(out, map[string]any{"type": "function_call", "id": "fc_" + randomID(), "call_id": c.ID, "name": c.Function.Name,
			"arguments": args, "status": "completed"})
	}
	return out
}

func responseObject(id, model, status string, created int64, output []map[string]any, u Usage, incomplete string) map[string]any {
	if output == nil {
		output = []map[string]any{}
	}
	obj := map[string]any{"id": id, "object": "response", "created_at": created, "model": model, "status": status,
		"output": output, "usage": responsesUsageFrom(u), "error": nil, "incomplete_details": nil}
	if incomplete != "" {
		obj["incomplete_details"] = map[string]string{"reason": incomplete}
	}
	return obj
}

// ChatToResponsesResponse converts a Chat response into a Responses response.
func ChatToResponsesResponse(body []byte, model string) ([]byte, Usage, error) {
	var r ChatResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, Usage{}, fmt.Errorf("上游响应不是合法的 Chat Completions JSON: %w", err)
	}
	if len(r.Choices) == 0 || r.Choices[0].Message == nil {
		return nil, Usage{}, fmt.Errorf("上游响应缺少 choices")
	}
	m := r.Choices[0].Message
	text, _ := chatContentText(m.Content, &issues{})
	status, incomplete := "completed", ""
	if fr := r.Choices[0].FinishReason; fr != nil && *fr == "length" {
		status, incomplete = "incomplete", "max_output_tokens"
	}
	var u Usage
	if r.Usage != nil {
		u = r.Usage.normalize()
	}
	b, err := json.Marshal(responseObject(respID(), model, status, time.Now().Unix(), chatOutputItems(chatReasoningBlocks(m), text, m.ToolCalls), u, incomplete))
	return b, u, err
}

// ---- streaming: Responses events -> Chat chunks ----

type responsesToChatStream struct {
	model        string
	includeUsage bool
	pivot        bool // emit x_og_reasoning for a composed converter
	id           string
	created      int64
	started      bool
	toolIndex    map[string]int // item id -> tool index
	nextTool     int
	usage        Usage
	hasUsage     bool
	out          int
	done         bool
}

func (s *responsesToChatStream) chunk(buf *bytes.Buffer, delta *ChatDelta, finish *string, usage *chatUsage) {
	choices := []ChatChoice{}
	if delta != nil || finish != nil {
		if delta == nil {
			delta = &ChatDelta{}
		}
		choices = append(choices, ChatChoice{Index: 0, Delta: delta, FinishReason: finish})
	}
	b, _ := json.Marshal(ChatResponse{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Choices: choices, Usage: usage})
	buf.Write(FormatEvent("", b))
}

func (s *responsesToChatStream) start(buf *bytes.Buffer) {
	if s.started {
		return
	}
	s.started = true
	if s.id == "" {
		s.id = "chatcmpl-" + randomID()
	}
	empty := ""
	s.chunk(buf, &ChatDelta{Role: "assistant", Content: &empty}, nil, nil)
}

func (s *responsesToChatStream) Process(ev *Event) ([]byte, error) {
	if len(bytes.TrimSpace(ev.Data)) == 0 {
		return nil, nil
	}
	var e struct {
		Type         string             `json:"type"`
		ItemID       string             `json:"item_id"`
		Delta        string             `json:"delta"`
		SummaryIndex int                `json:"summary_index"`
		Item         *responsesItem     `json:"item"`
		Response     *responsesResponse `json:"response"`
		Message      string             `json:"message"`
	}
	if err := json.Unmarshal(ev.Data, &e); err != nil {
		return nil, fmt.Errorf("上游流式数据不是合法 JSON: %w", err)
	}
	var buf bytes.Buffer
	switch e.Type {
	case "response.created":
		if e.Response != nil {
			s.id = chatID(strings.TrimPrefix(e.Response.ID, "resp_"))
		}
		s.start(&buf)
	case "response.output_text.delta":
		s.start(&buf)
		d := e.Delta
		s.out += len(d)
		s.chunk(&buf, &ChatDelta{Content: &d}, nil, nil)
	case "response.reasoning_summary_part.added":
		if e.SummaryIndex > 0 {
			sep := "\n\n"
			s.chunk(&buf, &ChatDelta{ReasoningContent: &sep}, nil, nil)
		}
	case "response.output_item.done":
		if s.pivot && e.Item != nil && e.Item.Type == "reasoning" && e.Item.EncryptedContent != "" {
			s.start(&buf)
			s.chunk(&buf, &ChatDelta{OGReasoning: []ReasoningBlock{{Provider: ReasoningOpenAI, ID: e.Item.ID, Blob: e.Item.EncryptedContent}}}, nil, nil)
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		s.start(&buf)
		d := e.Delta
		s.out += len(d)
		s.chunk(&buf, &ChatDelta{ReasoningContent: &d}, nil, nil)
	case "response.output_item.added":
		if e.Item != nil && e.Item.Type == "function_call" {
			s.start(&buf)
			idx := s.nextTool
			s.nextTool++
			s.toolIndex[e.Item.ID] = idx
			s.chunk(&buf, &ChatDelta{ToolCalls: []ChatToolCall{{Index: &idx, ID: e.Item.CallID, Type: "function",
				Function: ChatToolCallFunc{Name: e.Item.Name, Arguments: e.Item.Arguments}}}}, nil, nil)
		}
	case "response.function_call_arguments.delta":
		idx, ok := s.toolIndex[e.ItemID]
		if !ok {
			return nil, fmt.Errorf("上游流式数据中的函数参数没有对应的 function_call")
		}
		s.out += len(e.Delta)
		s.chunk(&buf, &ChatDelta{ToolCalls: []ChatToolCall{{Index: &idx, Function: ChatToolCallFunc{Arguments: e.Delta}}}}, nil, nil)
	case "response.completed", "response.incomplete":
		s.start(&buf)
		s.done = true
		finish := "stop"
		if e.Response != nil {
			if e.Response.Usage != nil {
				s.usage, s.hasUsage = e.Response.Usage.normalize(), true
			}
			if e.Response.IncompleteDetails != nil && e.Response.IncompleteDetails.Reason == "max_output_tokens" {
				finish = "length"
			}
		}
		if s.nextTool > 0 {
			finish = "tool_calls"
		}
		s.chunk(&buf, nil, &finish, nil)
		if s.includeUsage {
			s.chunk(&buf, nil, nil, chatUsageFrom(s.usage))
		}
		buf.Write(FormatEvent("", doneData))
	case "response.failed", "error":
		s.done = true
		msg := e.Message
		if msg == "" && e.Response != nil && len(e.Response.Error) > 0 {
			msg = UpstreamErrorMessage([]byte(`{"error":` + string(e.Response.Error) + `}`))
		}
		if msg == "" {
			msg = "upstream response failed"
		}
		buf.Write(EncodeStreamError(OpenAIChat, NewError(ErrUpstreamUnavailable, Redact(msg))))
		buf.Write(FormatEvent("", doneData))
	}
	return buf.Bytes(), nil
}

func (s *responsesToChatStream) Finish() []byte       { return nil }
func (s *responsesToChatStream) Usage() (Usage, bool) { return s.usage, s.hasUsage }
func (s *responsesToChatStream) OutputBytes() int     { return s.out }
func (s *responsesToChatStream) Done() bool           { return s.done }

// ---- streaming: Chat chunks -> Responses events ----

type chatToResponsesStream struct {
	model     string
	id        string
	created   int64
	seq       int
	started   bool
	outIndex  int
	open      string // "", "message", "reasoning", "function_call"
	itemID    string
	text      strings.Builder
	reasoning strings.Builder
	summary   bool            // the open reasoning item has a summary part
	blob      *ReasoningBlock // provider blob of the open reasoning item
	items     []map[string]any
	calls     []ChatToolCall
	callItem  map[int]string // chat tool index -> item id
	curTool   int
	finish    string
	usage     Usage
	hasUsage  bool
	out       int
	done      bool
	stopped   bool
}

func (s *chatToResponsesStream) emit(buf *bytes.Buffer, typ string, fields map[string]any) {
	fields["type"] = typ
	fields["sequence_number"] = s.seq
	s.seq++
	b, _ := json.Marshal(fields)
	buf.Write(FormatEvent(typ, b))
}

func (s *chatToResponsesStream) start(buf *bytes.Buffer) {
	if s.started {
		return
	}
	s.started = true
	r := responseObject(s.id, s.model, "in_progress", s.created, nil, Usage{}, "")
	r["usage"] = nil
	s.emit(buf, "response.created", map[string]any{"response": r})
	s.emit(buf, "response.in_progress", map[string]any{"response": r})
}

func (s *chatToResponsesStream) closeItem(buf *bytes.Buffer) {
	switch s.open {
	case "message":
		t := s.text.String()
		s.emit(buf, "response.output_text.done", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "content_index": 0, "text": t})
		part := map[string]any{"type": "output_text", "text": t, "annotations": []any{}}
		s.emit(buf, "response.content_part.done", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "content_index": 0, "part": part})
		item := map[string]any{"type": "message", "id": s.itemID, "role": "assistant", "status": "completed", "content": []any{part}}
		s.emit(buf, "response.output_item.done", map[string]any{"output_index": s.outIndex, "item": item})
		s.items = append(s.items, item)
		s.text.Reset()
	case "reasoning":
		t := s.reasoning.String()
		if s.summary {
			s.emit(buf, "response.reasoning_summary_text.done", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "summary_index": 0, "text": t})
			s.emit(buf, "response.reasoning_summary_part.done", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "summary_index": 0,
				"part": map[string]string{"type": "summary_text", "text": t}})
		}
		b := ReasoningBlock{Provider: ReasoningText, Text: t}
		if s.blob != nil {
			b = *s.blob
			b.Text = t
		}
		item := responsesReasoningItem(s.itemID, b)
		s.emit(buf, "response.output_item.done", map[string]any{"output_index": s.outIndex, "item": item})
		s.items = append(s.items, item)
		s.reasoning.Reset()
		s.summary, s.blob = false, nil
	case "function_call":
		c := s.calls[len(s.calls)-1]
		s.emit(buf, "response.function_call_arguments.done", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "arguments": c.Function.Arguments})
		item := map[string]any{"type": "function_call", "id": s.itemID, "call_id": c.ID, "name": c.Function.Name, "arguments": c.Function.Arguments, "status": "completed"}
		s.emit(buf, "response.output_item.done", map[string]any{"output_index": s.outIndex, "item": item})
		s.items = append(s.items, item)
	default:
		return
	}
	s.open = ""
	s.outIndex++
}

func (s *chatToResponsesStream) openItem(buf *bytes.Buffer, kind string, item map[string]any) {
	s.closeItem(buf)
	s.open = kind
	s.itemID, _ = item["id"].(string)
	s.emit(buf, "response.output_item.added", map[string]any{"output_index": s.outIndex, "item": item})
}

func (s *chatToResponsesStream) Process(ev *Event) ([]byte, error) {
	data := bytes.TrimSpace(ev.Data)
	if len(data) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	if bytes.Equal(data, doneData) {
		s.done = true
		return s.stop(&buf), nil
	}
	var chunk ChatResponse
	if err := json.Unmarshal(data, &chunk); err != nil {
		return nil, fmt.Errorf("上游流式数据不是合法 JSON: %w", err)
	}
	s.start(&buf)
	if chunk.Usage != nil {
		s.usage, s.hasUsage = chunk.Usage.normalize(), true
	}
	for _, c := range chunk.Choices {
		if d := c.Delta; d != nil {
			if r := d.reasoningText(); r != "" {
				if s.open != "reasoning" {
					s.openItem(&buf, "reasoning", map[string]any{"type": "reasoning", "id": "rs_" + randomID(), "summary": []any{}})
				}
				if !s.summary {
					s.summary = true
					s.emit(&buf, "response.reasoning_summary_part.added", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "summary_index": 0,
						"part": map[string]string{"type": "summary_text", "text": ""}})
				}
				s.reasoning.WriteString(r)
				s.out += len(r)
				s.emit(&buf, "response.reasoning_summary_text.delta", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "summary_index": 0, "delta": r})
			}
			// A provider blob ends its reasoning item (composed streams only).
			for _, b := range d.OGReasoning {
				if s.open != "reasoning" {
					s.openItem(&buf, "reasoning", map[string]any{"type": "reasoning", "id": "rs_" + randomID(), "summary": []any{}})
				}
				s.blob = &b
				s.closeItem(&buf)
			}
			if d.Content != nil && *d.Content != "" {
				if s.open != "message" {
					s.openItem(&buf, "message", map[string]any{"type": "message", "id": "msg_" + randomID(), "role": "assistant", "status": "in_progress", "content": []any{}})
					s.emit(&buf, "response.content_part.added", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "content_index": 0,
						"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
				}
				s.text.WriteString(*d.Content)
				s.out += len(*d.Content)
				s.emit(&buf, "response.output_text.delta", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "content_index": 0, "delta": *d.Content})
			}
			for _, tc := range d.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				if _, seen := s.callItem[idx]; !seen {
					id := tc.ID
					if id == "" {
						id = "call_" + randomID()
					}
					s.calls = append(s.calls, ChatToolCall{ID: id, Type: "function", Function: ChatToolCallFunc{Name: tc.Function.Name}})
					item := map[string]any{"type": "function_call", "id": "fc_" + randomID(), "call_id": id, "name": tc.Function.Name, "arguments": "", "status": "in_progress"}
					s.openItem(&buf, "function_call", item)
					s.callItem[idx], s.curTool = s.itemID, idx
				} else if s.open != "function_call" || s.curTool != idx {
					return nil, fmt.Errorf("上游流式工具调用参数交错，无法转换")
				}
				if tc.Function.Arguments != "" {
					s.calls[len(s.calls)-1].Function.Arguments += tc.Function.Arguments
					s.out += len(tc.Function.Arguments)
					s.emit(&buf, "response.function_call_arguments.delta", map[string]any{"item_id": s.itemID, "output_index": s.outIndex, "delta": tc.Function.Arguments})
				}
			}
		}
		if c.FinishReason != nil && *c.FinishReason != "" {
			s.finish = *c.FinishReason
		}
	}
	return buf.Bytes(), nil
}

func (s *chatToResponsesStream) stop(buf *bytes.Buffer) []byte {
	if s.stopped {
		return buf.Bytes()
	}
	s.stopped = true
	s.start(buf)
	s.closeItem(buf)
	status, incomplete := "completed", ""
	if s.finish == "length" {
		status, incomplete = "incomplete", "max_output_tokens"
	}
	output := s.items
	if len(output) == 0 {
		output = chatOutputItems(nil, "", nil)
	}
	final := responseObject(s.id, s.model, status, s.created, output, s.usage, incomplete)
	typ := "response.completed"
	if status == "incomplete" {
		typ = "response.incomplete"
	}
	s.emit(buf, typ, map[string]any{"response": final})
	return buf.Bytes()
}

func (s *chatToResponsesStream) Finish() []byte {
	if s.done || s.finish == "" {
		return nil
	}
	s.done = true
	var buf bytes.Buffer
	return s.stop(&buf)
}

func (s *chatToResponsesStream) Usage() (Usage, bool) { return s.usage, s.hasUsage }
func (s *chatToResponsesStream) OutputBytes() int     { return s.out }
func (s *chatToResponsesStream) Done() bool           { return s.done }
