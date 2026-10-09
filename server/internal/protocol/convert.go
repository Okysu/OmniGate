package protocol

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"
)

// Conversions between the three dialects. Chat Completions is the pivot:
// Messages <-> Responses is composed as Messages <-> Chat <-> Responses.

func mergeWarnings(a, b []string) []string {
	out := slices.Clone(a)
	for _, w := range b {
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

// ConversionHops is how many conversion steps a request needs between the
// client and upstream dialects: 0 = native passthrough, 1 = direct converter,
// 2 = composed through Chat (Messages <-> Responses). Routing prefers fewer
// hops so provider-specific features survive.
func ConversionHops(client, upstream string) int {
	switch {
	case client == upstream:
		return 0
	case client == Anthropic && upstream == OpenAIResponses, client == OpenAIResponses && upstream == Anthropic:
		return 2
	default:
		return 1
	}
}

// ConvertRequest converts a client request body into the upstream dialect.
// maxTokensField applies when the upstream is OpenAI Chat.
func ConvertRequest(client, upstream string, body []byte, upstreamModel, maxTokensField string, compat Compat) ([]byte, []string, error) {
	switch {
	case client == OpenAIChat && upstream == Anthropic:
		return ChatToAnthropicRequest(body, upstreamModel, compat)
	case client == Anthropic && upstream == OpenAIChat:
		return AnthropicToChatRequest(body, upstreamModel, maxTokensField, compat)
	case client == OpenAIChat && upstream == OpenAIResponses:
		return ChatToResponsesRequest(body, upstreamModel, compat)
	case client == OpenAIResponses && upstream == OpenAIChat:
		return ResponsesToChatRequest(body, upstreamModel, maxTokensField, compat)
	case client == Anthropic && upstream == OpenAIResponses:
		mid, w1, err := AnthropicToChatRequest(body, upstreamModel, "max_tokens", compat)
		if err != nil {
			return nil, nil, err
		}
		out, w2, err := ChatToResponsesRequest(mid, upstreamModel, compat)
		return out, mergeWarnings(w1, w2), err
	case client == OpenAIResponses && upstream == Anthropic:
		mid, w1, err := ResponsesToChatRequest(body, upstreamModel, "max_tokens", compat)
		if err != nil {
			return nil, nil, err
		}
		out, w2, err := ChatToAnthropicRequest(mid, upstreamModel, compat)
		return out, mergeWarnings(w1, w2), err
	}
	return nil, nil, &ConvertError{Class: ErrUnsupportedParameter, Message: fmt.Sprintf("不支持从 %s 转换到 %s", client, upstream)}
}

// ConvertResponse converts a non-streaming upstream response for the client.
func ConvertResponse(client, upstream string, body []byte, model string) ([]byte, Usage, error) {
	switch {
	case client == Anthropic && upstream == OpenAIChat:
		return ChatToAnthropicResponse(body, model)
	case client == OpenAIChat && upstream == Anthropic:
		return AnthropicToChatResponse(body, model)
	case client == OpenAIChat && upstream == OpenAIResponses:
		return ResponsesToChatResponse(body, model)
	case client == OpenAIResponses && upstream == OpenAIChat:
		return ChatToResponsesResponse(body, model)
	case client == Anthropic && upstream == OpenAIResponses:
		mid, u, err := ResponsesToChatResponse(body, model)
		if err != nil {
			return nil, u, err
		}
		out, _, err := ChatToAnthropicResponse(mid, model)
		return out, u, err
	case client == OpenAIResponses && upstream == Anthropic:
		mid, u, err := AnthropicToChatResponse(body, model)
		if err != nil {
			return nil, u, err
		}
		out, _, err := ChatToResponsesResponse(mid, model)
		return out, u, err
	}
	return nil, Usage{}, fmt.Errorf("protocol: no response conversion from %s to %s", upstream, client)
}

// composedStream pipes upstream events through first (upstream -> chat) and
// then second (chat -> client).
type composedStream struct {
	first, second StreamProcessor
}

func (c *composedStream) pipe(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out bytes.Buffer
	r := NewSSEReader(bytes.NewReader(b))
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return out.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		o, err := c.second.Process(ev)
		if err != nil {
			return nil, err
		}
		out.Write(o)
	}
}

func (c *composedStream) Process(ev *Event) ([]byte, error) {
	mid, err := c.first.Process(ev)
	if err != nil {
		return nil, err
	}
	return c.pipe(mid)
}

func (c *composedStream) Finish() []byte {
	out, _ := c.pipe(c.first.Finish())
	return append(out, c.second.Finish()...)
}

func (c *composedStream) Usage() (Usage, bool) { return c.first.Usage() }
func (c *composedStream) OutputBytes() int     { return c.first.OutputBytes() }
func (c *composedStream) Done() bool           { return c.second.Done() }

func newResponsesToChat(model string, includeUsage bool) *responsesToChatStream {
	return &responsesToChatStream{model: model, includeUsage: includeUsage, toolIndex: map[string]int{}, created: time.Now().Unix()}
}

func newChatToResponses(model string) *chatToResponsesStream {
	return &chatToResponsesStream{model: model, id: respID(), created: time.Now().Unix(), callItem: map[int]string{}}
}

func newAnthropicToChat(model string, includeUsage bool) *anthropicToChatStream {
	return &anthropicToChatStream{model: model, includeUsage: includeUsage, toolIndex: map[int]int{}, created: time.Now().Unix()}
}
