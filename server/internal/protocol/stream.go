package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// StreamProcessor turns upstream SSE events into client bytes while metering usage.
type StreamProcessor interface {
	// Process handles one upstream event and returns bytes to send (may be nil).
	Process(ev *Event) ([]byte, error)
	// Finish is called once at upstream EOF and returns trailing bytes.
	Finish() []byte
	// Usage returns the usage seen so far and whether the upstream reported it.
	Usage() (Usage, bool)
	// OutputBytes is the amount of generated text/arguments seen (for estimation).
	OutputBytes() int
	// Done reports whether the upstream's terminal event was seen.
	Done() bool
}

// NewStreamProcessor picks the processor for client dialect `client` reading an
// upstream speaking `upstream`. model is the client-facing model name.
func NewStreamProcessor(client, upstream, model string, includeUsage bool) (StreamProcessor, error) {
	switch {
	case client == upstream && client == OpenAIChat:
		return &chatPassthrough{includeUsage: includeUsage}, nil
	case client == upstream && client == OpenAICompletions:
		return &completionPassthrough{includeUsage: includeUsage}, nil
	case client == upstream && client == Anthropic:
		return &anthropicPassthrough{}, nil
	case client == upstream && client == OpenAIResponses:
		return &responsesPassthrough{}, nil
	case client == Anthropic && upstream == OpenAIChat:
		return &chatToAnthropicStream{model: model, toolBlocks: map[int]int{}}, nil
	case client == OpenAIChat && upstream == Anthropic:
		return newAnthropicToChat(model, includeUsage), nil
	case client == OpenAIChat && upstream == OpenAIResponses:
		return newResponsesToChat(model, includeUsage), nil
	case client == OpenAIResponses && upstream == OpenAIChat:
		return newChatToResponses(model), nil
	case client == Anthropic && upstream == OpenAIResponses:
		return &composedStream{first: newResponsesToChat(model, true), second: &chatToAnthropicStream{model: model, toolBlocks: map[int]int{}}}, nil
	case client == OpenAIResponses && upstream == Anthropic:
		return &composedStream{first: newAnthropicToChat(model, true), second: newChatToResponses(model)}, nil
	}
	return nil, fmt.Errorf("protocol: no stream conversion from %s to %s", upstream, client)
}

var doneData = []byte("[DONE]")

// ---- passthrough ----

type chatPassthrough struct {
	includeUsage bool
	usage        Usage
	hasUsage     bool
	out          int
	done         bool
}

func (p *chatPassthrough) Process(ev *Event) ([]byte, error) {
	if bytes.Equal(bytes.TrimSpace(ev.Data), doneData) {
		p.done = true
		return ev.Raw, nil
	}
	var chunk ChatResponse
	if len(ev.Data) > 0 && json.Unmarshal(ev.Data, &chunk) == nil {
		for _, c := range chunk.Choices {
			if c.Delta != nil {
				if c.Delta.Content != nil {
					p.out += len(*c.Delta.Content)
				}
				p.out += len(c.Delta.reasoningText())
				for _, tc := range c.Delta.ToolCalls {
					p.out += len(tc.Function.Arguments)
				}
			}
		}
		if chunk.Usage != nil {
			p.usage, p.hasUsage = chunk.Usage.normalize(), true
			// The gateway forced include_usage; hide the usage-only chunk from clients that didn't ask.
			if !p.includeUsage && len(chunk.Choices) == 0 {
				return nil, nil
			}
		}
	}
	return ev.Raw, nil
}

func (p *chatPassthrough) Finish() []byte       { return nil }
func (p *chatPassthrough) Usage() (Usage, bool) { return p.usage, p.hasUsage }
func (p *chatPassthrough) OutputBytes() int     { return p.out }
func (p *chatPassthrough) Done() bool           { return p.done }

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID    string         `json:"id"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message,omitempty"`
	Index        int             `json:"index"`
	ContentBlock *AnthropicBlock `json:"content_block,omitempty"`
	Delta        *struct {
		Type        string  `json:"type"`
		Text        string  `json:"text"`
		Thinking    string  `json:"thinking"`
		PartialJSON string  `json:"partial_json"`
		StopReason  *string `json:"stop_reason"`
	} `json:"delta,omitempty"`
	Usage *anthropicUsage `json:"usage,omitempty"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// anthropicMeter accumulates usage across message_start and message_delta.
type anthropicMeter struct {
	usage    Usage
	hasUsage bool
	out      int
}

func (m *anthropicMeter) observe(e *anthropicStreamEvent) {
	switch e.Type {
	case "message_start":
		if e.Message != nil {
			m.usage = e.Message.Usage.normalize()
			m.hasUsage = true
		}
	case "message_delta":
		if e.Usage != nil {
			// message_delta usage is cumulative; input fields may be present too.
			m.usage.Output = e.Usage.OutputTokens
			if e.Usage.InputTokens > 0 {
				m.usage.Input = e.Usage.InputTokens
			}
			if e.Usage.CacheReadInputTokens > 0 {
				m.usage.CacheRead = e.Usage.CacheReadInputTokens
			}
			if e.Usage.CacheCreationInputTokens > 0 {
				m.usage.CacheWrite = e.Usage.CacheCreationInputTokens
			}
			m.hasUsage = true
		}
	case "content_block_delta":
		if e.Delta != nil {
			m.out += len(e.Delta.Text) + len(e.Delta.Thinking) + len(e.Delta.PartialJSON)
		}
	}
}

type anthropicPassthrough struct {
	meter anthropicMeter
	done  bool
}

func (p *anthropicPassthrough) Process(ev *Event) ([]byte, error) {
	var e anthropicStreamEvent
	if len(ev.Data) > 0 && json.Unmarshal(ev.Data, &e) == nil {
		p.meter.observe(&e)
		if e.Type == "message_stop" || e.Type == "error" {
			p.done = true
		}
	}
	return ev.Raw, nil
}

func (p *anthropicPassthrough) Finish() []byte       { return nil }
func (p *anthropicPassthrough) Usage() (Usage, bool) { return p.meter.usage, p.meter.hasUsage }
func (p *anthropicPassthrough) OutputBytes() int     { return p.meter.out }
func (p *anthropicPassthrough) Done() bool           { return p.done }

type responsesPassthrough struct {
	usage    Usage
	hasUsage bool
	out      int
	done     bool
}

func (p *responsesPassthrough) Process(ev *Event) ([]byte, error) {
	var e struct {
		Type     string `json:"type"`
		Delta    string `json:"delta"`
		Response *struct {
			Usage *responsesUsage `json:"usage"`
		} `json:"response"`
	}
	if len(ev.Data) > 0 && json.Unmarshal(ev.Data, &e) == nil {
		p.out += len(e.Delta)
		switch e.Type {
		case "response.completed", "response.incomplete", "response.failed":
			p.done = true
			if e.Response != nil && e.Response.Usage != nil {
				p.usage, p.hasUsage = e.Response.Usage.normalize(), true
			}
		case "error":
			p.done = true
		}
	}
	return ev.Raw, nil
}

func (p *responsesPassthrough) Finish() []byte       { return nil }
func (p *responsesPassthrough) Usage() (Usage, bool) { return p.usage, p.hasUsage }
func (p *responsesPassthrough) OutputBytes() int     { return p.out }
func (p *responsesPassthrough) Done() bool           { return p.done }

// ---- OpenAI Chat stream -> Anthropic Messages stream ----

type chatToAnthropicStream struct {
	model      string
	started    bool
	blockOpen  bool
	blockType  string // "text" | "thinking" | "tool_use"
	blockIndex int
	nextIndex  int
	curTool    int         // OpenAI tool_call index of the open tool block
	toolBlocks map[int]int // OpenAI tool index -> Anthropic block index
	finish     string
	usage      Usage
	hasUsage   bool
	out        int
	done       bool
	stopped    bool
}

func (s *chatToAnthropicStream) emit(buf *bytes.Buffer, name string, v any) {
	b, _ := json.Marshal(v)
	buf.Write(FormatEvent(name, b))
}

func (s *chatToAnthropicStream) start(buf *bytes.Buffer, id string) {
	if s.started {
		return
	}
	s.started = true
	s.emit(buf, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": anthropicID(id), "type": "message", "role": "assistant", "model": s.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int64{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

func (s *chatToAnthropicStream) closeBlock(buf *bytes.Buffer) {
	if s.blockOpen {
		s.emit(buf, "content_block_stop", map[string]any{"type": "content_block_stop", "index": s.blockIndex})
		s.blockOpen = false
	}
}

func (s *chatToAnthropicStream) openBlock(buf *bytes.Buffer, typ string, block map[string]any) {
	s.closeBlock(buf)
	s.blockIndex, s.blockType, s.blockOpen = s.nextIndex, typ, true
	s.nextIndex++
	s.emit(buf, "content_block_start", map[string]any{"type": "content_block_start", "index": s.blockIndex, "content_block": block})
}

func (s *chatToAnthropicStream) Process(ev *Event) ([]byte, error) {
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
	s.start(&buf, chunk.ID)
	if chunk.Usage != nil {
		s.usage, s.hasUsage = chunk.Usage.normalize(), true
	}
	for _, c := range chunk.Choices {
		if c.Delta != nil {
			d := c.Delta
			if r := d.reasoningText(); r != "" {
				if !s.blockOpen || s.blockType != "thinking" {
					s.openBlock(&buf, "thinking", map[string]any{"type": "thinking", "thinking": ""})
				}
				s.out += len(r)
				s.emit(&buf, "content_block_delta", map[string]any{"type": "content_block_delta", "index": s.blockIndex,
					"delta": map[string]string{"type": "thinking_delta", "thinking": r}})
			}
			if d.Content != nil && *d.Content != "" {
				if !s.blockOpen || s.blockType != "text" {
					s.openBlock(&buf, "text", map[string]any{"type": "text", "text": ""})
				}
				s.out += len(*d.Content)
				s.emit(&buf, "content_block_delta", map[string]any{"type": "content_block_delta", "index": s.blockIndex,
					"delta": map[string]string{"type": "text_delta", "text": *d.Content}})
			}
			for _, tc := range d.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				if _, seen := s.toolBlocks[idx]; !seen {
					id := tc.ID
					if id == "" {
						id = "toolu_" + randomID()
					}
					s.openBlock(&buf, "tool_use", map[string]any{"type": "tool_use", "id": id, "name": tc.Function.Name, "input": map[string]any{}})
					s.toolBlocks[idx], s.curTool = s.blockIndex, idx
				} else if !s.blockOpen || s.blockType != "tool_use" || s.curTool != idx {
					// Interleaved fragments for an already closed tool block cannot be expressed.
					return nil, fmt.Errorf("上游流式工具调用参数交错，无法转换")
				}
				if tc.Function.Arguments != "" {
					s.out += len(tc.Function.Arguments)
					s.emit(&buf, "content_block_delta", map[string]any{"type": "content_block_delta", "index": s.blockIndex,
						"delta": map[string]string{"type": "input_json_delta", "partial_json": tc.Function.Arguments}})
				}
			}
		}
		if c.FinishReason != nil && *c.FinishReason != "" {
			s.finish = *c.FinishReason
		}
	}
	return buf.Bytes(), nil
}

func (s *chatToAnthropicStream) stop(buf *bytes.Buffer) []byte {
	if s.stopped {
		return buf.Bytes()
	}
	s.stopped = true
	s.start(buf, "")
	s.closeBlock(buf)
	s.emit(buf, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": chatFinishToAnthropic(s.finish), "stop_sequence": nil},
		"usage": anthropicUsageFrom(s.usage),
	})
	s.emit(buf, "message_stop", map[string]any{"type": "message_stop"})
	return buf.Bytes()
}

// Finish synthesizes the closing events if the upstream ended without [DONE]
// after a finish_reason (some providers omit [DONE]).
func (s *chatToAnthropicStream) Finish() []byte {
	if s.done || s.finish == "" {
		return nil
	}
	s.done = true
	var buf bytes.Buffer
	return s.stop(&buf)
}

func (s *chatToAnthropicStream) Usage() (Usage, bool) { return s.usage, s.hasUsage }
func (s *chatToAnthropicStream) OutputBytes() int     { return s.out }
func (s *chatToAnthropicStream) Done() bool           { return s.done }

// ---- Anthropic Messages stream -> OpenAI Chat stream ----

type anthropicToChatStream struct {
	model        string
	includeUsage bool
	id           string
	created      int64
	meter        anthropicMeter
	toolIndex    map[int]int // anthropic block index -> openai tool index
	nextTool     int
	finish       string
	done         bool
}

func (s *anthropicToChatStream) chunk(buf *bytes.Buffer, delta *ChatDelta, finish *string, usage *chatUsage) {
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

func (s *anthropicToChatStream) Process(ev *Event) ([]byte, error) {
	if len(bytes.TrimSpace(ev.Data)) == 0 {
		return nil, nil
	}
	var e anthropicStreamEvent
	if err := json.Unmarshal(ev.Data, &e); err != nil {
		return nil, fmt.Errorf("上游流式数据不是合法 JSON: %w", err)
	}
	s.meter.observe(&e)
	var buf bytes.Buffer
	switch e.Type {
	case "message_start":
		id := ""
		if e.Message != nil {
			id = e.Message.ID
		}
		s.id = chatID(id)
		empty := ""
		s.chunk(&buf, &ChatDelta{Role: "assistant", Content: &empty}, nil, nil)
	case "content_block_start":
		if e.ContentBlock != nil && e.ContentBlock.Type == "tool_use" {
			idx := s.nextTool
			s.nextTool++
			s.toolIndex[e.Index] = idx
			s.chunk(&buf, &ChatDelta{ToolCalls: []ChatToolCall{{Index: &idx, ID: e.ContentBlock.ID, Type: "function",
				Function: ChatToolCallFunc{Name: e.ContentBlock.Name, Arguments: ""}}}}, nil, nil)
		}
	case "content_block_delta":
		if e.Delta == nil {
			break
		}
		switch e.Delta.Type {
		case "text_delta":
			t := e.Delta.Text
			s.chunk(&buf, &ChatDelta{Content: &t}, nil, nil)
		case "thinking_delta":
			t := e.Delta.Thinking
			s.chunk(&buf, &ChatDelta{ReasoningContent: &t}, nil, nil)
		case "input_json_delta":
			idx, ok := s.toolIndex[e.Index]
			if !ok {
				return nil, fmt.Errorf("上游流式数据中的工具参数没有对应的 tool_use 块")
			}
			s.chunk(&buf, &ChatDelta{ToolCalls: []ChatToolCall{{Index: &idx, Function: ChatToolCallFunc{Arguments: e.Delta.PartialJSON}}}}, nil, nil)
		}
	case "message_delta":
		if e.Delta != nil && e.Delta.StopReason != nil {
			s.finish = anthropicStopToChat(*e.Delta.StopReason)
		}
	case "message_stop":
		s.done = true
		finish := s.finish
		if finish == "" {
			finish = "stop"
		}
		s.chunk(&buf, nil, &finish, nil)
		if s.includeUsage {
			s.chunk(&buf, nil, nil, chatUsageFrom(s.meter.usage))
		}
		buf.Write(FormatEvent("", doneData))
	case "error":
		s.done = true
		msg := "upstream stream error"
		if e.Error != nil {
			msg = e.Error.Message
		}
		buf.Write(EncodeStreamError(OpenAIChat, NewError(ErrUpstreamUnavailable, Redact(msg))))
		buf.Write(FormatEvent("", doneData))
	}
	return buf.Bytes(), nil
}

func (s *anthropicToChatStream) Finish() []byte       { return nil }
func (s *anthropicToChatStream) Usage() (Usage, bool) { return s.meter.usage, s.meter.hasUsage }
func (s *anthropicToChatStream) OutputBytes() int     { return s.meter.out }
func (s *anthropicToChatStream) Done() bool           { return s.done }
