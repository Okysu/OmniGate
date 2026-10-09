package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Custom is the upstream dialect of channels whose plugin implements the
// upstream protocol (phase9-api.md §2). The gateway talks to the plugin in
// Chat Completions (the pivot) and converts for the client.
const Custom = "custom"

// CanonicalEvent is a plugin stream event: a subset of a Chat Completions
// chunk (phase9-api.md §2).
type CanonicalEvent struct {
	Type      string          `json:"type"` // delta | finish | usage | error
	Content   *string         `json:"content,omitempty"`
	Reasoning *string         `json:"reasoning,omitempty"`
	ToolCalls []ChatToolCall  `json:"toolCalls,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	Message   string          `json:"message,omitempty"`
	// Status optionally classifies an error event (default 502).
	Status int `json:"status,omitempty"`
}

// CanonicalError is an upstream error reported by a plugin (an error event,
// or a CanonicalResponse's error).
type CanonicalError struct {
	Status  int
	Message string
}

func (e *CanonicalError) Error() string { return e.Message }

// CanonicalStream turns plugin events into client-protocol bytes: events are
// rendered as Chat Completions chunks and fed to the Chat → client stream
// processor.
type CanonicalStream struct {
	proc     StreamProcessor
	id       string
	created  int64
	model    string
	roleSent bool
	finished bool
	usage    *chatUsage
	toolIdx  map[string]int // tool call id -> index, for calls without index
	nextTool int
	lastTool int
	ended    bool
}

// NewCanonicalStream renders events for a client speaking dialect client.
// The usage chunk is always produced; Chat clients that did not ask for usage
// don't see it (includeUsage).
func NewCanonicalStream(client, model string, includeUsage bool) (*CanonicalStream, error) {
	proc, err := NewStreamProcessor(client, OpenAIChat, model, includeUsage)
	if err != nil {
		return nil, err
	}
	return &CanonicalStream{proc: proc, id: chatID(""), created: time.Now().Unix(), model: model, toolIdx: map[string]int{}, lastTool: -1}, nil
}

func (s *CanonicalStream) feed(buf *bytes.Buffer, chunk map[string]any) error {
	b, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	return s.feedRaw(buf, b)
}

func (s *CanonicalStream) feedRaw(buf *bytes.Buffer, data []byte) error {
	out, err := s.proc.Process(&Event{Data: data, Raw: FormatEvent("", data)})
	if err != nil {
		return err
	}
	buf.Write(out)
	return nil
}

func (s *CanonicalStream) chunk(choices []map[string]any) map[string]any {
	if choices == nil {
		choices = []map[string]any{}
	}
	return map[string]any{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": choices}
}

// Events converts a batch of events. An error event is returned as a
// *CanonicalError (bytes produced before it are returned too); other errors
// mean the events are malformed.
func (s *CanonicalStream) Events(evs []CanonicalEvent) ([]byte, error) {
	var buf bytes.Buffer
	for _, e := range evs {
		if s.ended {
			break
		}
		switch e.Type {
		case "delta":
			if s.finished {
				continue // content after finish is ignored
			}
			delta := map[string]any{}
			if !s.roleSent {
				delta["role"] = "assistant"
			}
			if e.Reasoning != nil && *e.Reasoning != "" {
				delta["reasoning_content"] = *e.Reasoning
			}
			if e.Content != nil && (*e.Content != "" || !s.roleSent) {
				delta["content"] = *e.Content
			}
			if len(e.ToolCalls) > 0 {
				calls, err := s.toolCalls(e.ToolCalls)
				if err != nil {
					return buf.Bytes(), err
				}
				delta["tool_calls"] = calls
			}
			if len(delta) == 0 || (len(delta) == 1 && delta["role"] != nil) {
				continue // nothing to say yet
			}
			s.roleSent = true
			if err := s.feed(&buf, s.chunk([]map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}})); err != nil {
				return buf.Bytes(), err
			}
		case "finish":
			if s.finished {
				continue
			}
			if err := s.finish(&buf, e.Reason); err != nil {
				return buf.Bytes(), err
			}
		case "usage":
			var u chatUsage
			if err := json.Unmarshal(e.Usage, &u); err != nil || len(bytes.TrimSpace(e.Usage)) == 0 {
				return buf.Bytes(), fmt.Errorf("usage 事件需要 Chat Completions 格式的 usage（prompt_tokens、completion_tokens）")
			}
			if u.PromptTokens < 0 || u.CompletionTokens < 0 {
				return buf.Bytes(), fmt.Errorf("usage 不能为负数")
			}
			u.TotalTokens = u.PromptTokens + u.CompletionTokens
			s.usage = &u
		case "error":
			msg := strings.TrimSpace(e.Message)
			if msg == "" {
				msg = "upstream stream error"
			}
			return buf.Bytes(), &CanonicalError{Status: e.Status, Message: msg}
		default:
			return buf.Bytes(), fmt.Errorf("未知的事件类型 %q（delta / finish / usage / error）", e.Type)
		}
	}
	return buf.Bytes(), nil
}

func (s *CanonicalStream) toolCalls(in []ChatToolCall) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(in))
	for _, tc := range in {
		idx := -1
		switch {
		case tc.Index != nil:
			idx = *tc.Index
		case tc.ID != "":
			if i, ok := s.toolIdx[tc.ID]; ok {
				idx = i
			} else {
				idx = s.nextTool
			}
		case s.lastTool >= 0:
			idx = s.lastTool // continuation of the current call
		default:
			idx = 0
		}
		if idx < 0 || idx > 1000 {
			return nil, fmt.Errorf("toolCalls 的 index 无效")
		}
		if tc.ID != "" {
			s.toolIdx[tc.ID] = idx
		}
		s.nextTool = max(s.nextTool, idx+1)
		s.lastTool = idx
		call := map[string]any{"index": idx, "function": map[string]any{"arguments": tc.Function.Arguments}}
		if tc.ID != "" {
			call["id"] = tc.ID
			call["type"] = "function"
		}
		if tc.Function.Name != "" {
			call["function"].(map[string]any)["name"] = tc.Function.Name
			call["type"] = "function"
		}
		out = append(out, call)
	}
	return out, nil
}

func (s *CanonicalStream) finish(buf *bytes.Buffer, reason string) error {
	s.finished = true
	switch reason {
	case "stop", "length", "tool_calls", "content_filter":
	case "":
		reason = "stop"
	}
	if !s.roleSent {
		s.roleSent = true
		if err := s.feed(buf, s.chunk([]map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil}})); err != nil {
			return err
		}
	}
	return s.feed(buf, s.chunk([]map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": reason}}))
}

// Finish ends the stream at upstream EOF: a finish chunk when no finish event
// was seen ("stop"), the usage chunk and [DONE].
func (s *CanonicalStream) Finish() ([]byte, error) {
	if s.ended {
		return nil, nil
	}
	var buf bytes.Buffer
	if !s.finished {
		if err := s.finish(&buf, "stop"); err != nil {
			return buf.Bytes(), err
		}
	}
	s.ended = true
	if s.usage != nil {
		c := s.chunk(nil)
		c["usage"] = s.usage
		if err := s.feed(&buf, c); err != nil {
			return buf.Bytes(), err
		}
	}
	if err := s.feedRaw(&buf, doneData); err != nil {
		return buf.Bytes(), err
	}
	buf.Write(s.proc.Finish())
	return buf.Bytes(), nil
}

// Usage returns the usage reported by usage events.
func (s *CanonicalStream) Usage() (Usage, bool) {
	if s.usage == nil {
		return Usage{}, false
	}
	return s.usage.normalize(), true
}

// OutputBytes is the generated text seen (for estimation).
func (s *CanonicalStream) OutputBytes() int { return s.proc.OutputBytes() }

// Done reports whether the client stream was completed.
func (s *CanonicalStream) Done() bool { return s.ended && s.proc.Done() }

// canonicalResponse is the parseResponse result (Chat Completions subset).
type canonicalResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        *int         `json:"index"`
		Message      *ChatMessage `json:"message"`
		FinishReason *string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
	Error *struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

// NormalizeCanonicalResponse completes a parseResponse result into a Chat
// Completions response for model (id, object, created, model, choice index,
// role and finish_reason are filled in). A response carrying error yields a
// *CanonicalError; usage reports whether usage was present.
func NormalizeCanonicalResponse(raw []byte, model string) (out []byte, usage Usage, hasUsage bool, err error) {
	var r canonicalResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, Usage{}, false, fmt.Errorf("parseResponse 必须返回 Chat Completions 响应对象")
	}
	if r.Error != nil {
		msg := strings.TrimSpace(r.Error.Message)
		if msg == "" {
			msg = "upstream error"
		}
		return nil, Usage{}, false, &CanonicalError{Status: r.Error.Status, Message: msg}
	}
	if len(r.Choices) == 0 || r.Choices[0].Message == nil {
		return nil, Usage{}, false, fmt.Errorf("parseResponse 的结果缺少 choices[0].message")
	}
	resp := ChatResponse{ID: r.ID, Object: "chat.completion", Created: time.Now().Unix(), Model: model, Usage: r.Usage}
	if !strings.HasPrefix(resp.ID, "chatcmpl") {
		resp.ID = chatID(resp.ID)
	}
	for i, c := range r.Choices {
		m := *c.Message
		m.Role = "assistant"
		if len(m.Content) == 0 {
			m.Content = json.RawMessage("null")
		}
		for j := range m.ToolCalls {
			m.ToolCalls[j].Index = nil
			if m.ToolCalls[j].Type == "" {
				m.ToolCalls[j].Type = "function"
			}
			if m.ToolCalls[j].ID == "" {
				m.ToolCalls[j].ID = "call_" + randomID()
			}
		}
		finish := "stop"
		if len(m.ToolCalls) > 0 {
			finish = "tool_calls"
		}
		if c.FinishReason != nil && *c.FinishReason != "" {
			finish = *c.FinishReason
		}
		idx := i
		if c.Index != nil {
			idx = *c.Index
		}
		resp.Choices = append(resp.Choices, ChatChoice{Index: idx, Message: &m, FinishReason: &finish})
	}
	if r.Usage != nil {
		r.Usage.TotalTokens = r.Usage.PromptTokens + r.Usage.CompletionTokens
		usage, hasUsage = r.Usage.normalize(), true
	}
	out, err = json.Marshal(resp)
	return out, usage, hasUsage, err
}
