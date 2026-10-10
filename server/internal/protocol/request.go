package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// Compat controls what happens to request fields that cannot be converted.
type Compat int

const (
	// Strict rejects the request with unsupported_parameter.
	Strict Compat = iota
	// Lenient drops the fields and reports them in X-OmniGate-Compat-Warnings.
	Lenient
)

// ConvertError is a client-side conversion failure.
type ConvertError struct {
	Class   string // "invalid_request" | "unsupported_parameter"
	Message string
	Fields  []string
}

func (e *ConvertError) Error() string { return e.Message }

func invalid(format string, a ...any) *ConvertError {
	return &ConvertError{Class: "invalid_request", Message: fmt.Sprintf(format, a...)}
}

// RequestInfo is the minimal information the router needs from any request body.
type RequestInfo struct {
	Model        string
	Stream       bool
	MaxTokens    int64 // 0 = not specified; at most MaxTokensLimit
	IncludeUsage bool  // OpenAI chat / completions: client asked for stream usage
	BodyBytes    int
	// Image endpoints: requested image count (n, default 1) and prompt size.
	Images      int64
	PromptBytes int
	// Characters is the input size of a speech request in Unicode code points.
	Characters int64
}

// MaxTokensLimit bounds the output token count the gateway takes from a
// request's max_tokens / max_completion_tokens / max_output_tokens for its
// balance estimate (larger values are clamped, so price arithmetic cannot
// overflow). The request body itself is forwarded unchanged.
const MaxTokensLimit = 1_000_000

// ParseInfo extracts RequestInfo from a request body of the given dialect.
func ParseInfo(dialect string, body []byte) (RequestInfo, error) {
	var r struct {
		Model               string         `json:"model"`
		Stream              bool           `json:"stream"`
		MaxTokens           *int64         `json:"max_tokens"`
		MaxCompletionTokens *int64         `json:"max_completion_tokens"`
		MaxOutputTokens     *int64         `json:"max_output_tokens"`
		StreamOptions       *StreamOptions `json:"stream_options"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return RequestInfo{}, invalid("请求体不是合法的 JSON：%v", err)
	}
	if strings.TrimSpace(r.Model) == "" {
		return RequestInfo{}, invalid("缺少 model 字段")
	}
	info := RequestInfo{Model: r.Model, Stream: r.Stream, BodyBytes: len(body)}
	for _, v := range []*int64{r.MaxCompletionTokens, r.MaxTokens, r.MaxOutputTokens} {
		if v != nil && *v > 0 {
			info.MaxTokens = min(*v, MaxTokensLimit)
			break
		}
	}
	if r.StreamOptions != nil {
		info.IncludeUsage = r.StreamOptions.IncludeUsage
	}
	if dialect == Anthropic && info.MaxTokens == 0 {
		return RequestInfo{}, invalid("max_tokens 为必填字段")
	}
	return info, nil
}

// RewriteForPassthrough replaces "model" and, for streaming chat and
// completions, forces stream_options.include_usage so usage can be metered.
// All other fields are preserved byte-for-byte.
func RewriteForPassthrough(dialect string, body []byte, upstreamModel string, stream bool) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, invalid("请求体不是合法的 JSON：%v", err)
	}
	mb, _ := json.Marshal(upstreamModel)
	m["model"] = mb
	if (dialect == OpenAIChat || dialect == OpenAICompletions) && stream {
		var so map[string]json.RawMessage
		if raw, ok := m["stream_options"]; ok && !isNull(raw) {
			if err := json.Unmarshal(raw, &so); err != nil {
				return nil, invalid("stream_options 格式错误")
			}
		}
		if so == nil {
			so = map[string]json.RawMessage{}
		}
		so["include_usage"] = json.RawMessage("true")
		m["stream_options"], _ = json.Marshal(so)
	}
	return json.Marshal(m)
}

// SetPromptCacheKey sets prompt_cache_key on an OpenAI Chat / Responses
// request body that has no non-empty one (session affinity's
// inject_prompt_cache_key, phase12-api.md §2.6); a value the client sent is
// never replaced. Without the field the key is inserted as the first member
// and the rest of the body is kept byte for byte; an empty or null field is
// replaced through a re-encode like RewriteForPassthrough.
func SetPromptCacheKey(body []byte, key string) ([]byte, error) {
	cur := gjson.GetBytes(body, "prompt_cache_key")
	if cur.Exists() && cur.Type != gjson.Null && !(cur.Type == gjson.String && cur.Str == "") {
		return body, nil
	}
	kb, _ := json.Marshal(key)
	if cur.Exists() {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, invalid("请求体不是合法的 JSON：%v", err)
		}
		m["prompt_cache_key"] = kb
		return json.Marshal(m)
	}
	rest := bytes.TrimLeft(body, " \t\r\n")
	if len(rest) == 0 || rest[0] != '{' {
		return nil, invalid("请求体必须是 JSON 对象")
	}
	rest = rest[1:]
	out := make([]byte, 0, len(body)+len(kb)+24)
	out = append(out, `{"prompt_cache_key":`...)
	out = append(out, kb...)
	if t := bytes.TrimLeft(rest, " \t\r\n"); len(t) > 0 && t[0] != '}' {
		out = append(out, ',')
	}
	return append(out, rest...), nil
}

// fieldPolicy classifies top-level fields during conversion.
type fieldPolicy struct {
	handled map[string]bool
	// neutral fields are ignored when their value is a no-op default.
	neutral map[string]func(json.RawMessage) bool
	// hints are always dropped with a warning (they change cost/latency, not meaning).
	hints map[string]bool
}

type issues struct {
	unsupported []string
	dropped     []string
}

func (is *issues) unsupportedField(f string) {
	if !slices.Contains(is.unsupported, f) {
		is.unsupported = append(is.unsupported, f)
	}
}

func (is *issues) drop(f string) {
	if !slices.Contains(is.dropped, f) {
		is.dropped = append(is.dropped, f)
	}
}

// resolve turns issues into warnings or an error according to compat.
func (is *issues) resolve(compat Compat, target string) ([]string, error) {
	sort.Strings(is.unsupported)
	if len(is.unsupported) > 0 && compat == Strict {
		return nil, &ConvertError{
			Class:   "unsupported_parameter",
			Message: fmt.Sprintf("以下字段无法转换为 %s 协议：%s（可将 Key 的兼容模式设为 lenient 以丢弃这些字段）", target, strings.Join(is.unsupported, ", ")),
			Fields:  is.unsupported,
		}
	}
	w := append(slices.Clone(is.dropped), is.unsupported...)
	sort.Strings(w)
	return w, nil
}

func (p fieldPolicy) check(raw map[string]json.RawMessage, is *issues) {
	for k, v := range raw {
		switch {
		case p.handled[k]:
		case p.hints[k]:
			if !isNull(v) {
				is.drop(k)
			}
		case p.neutral[k] != nil:
			if !p.neutral[k](v) {
				is.unsupportedField(k)
			}
		default:
			if !isNull(v) {
				is.unsupportedField(k)
			}
		}
	}
}

func isNull(v json.RawMessage) bool { return len(v) == 0 || string(bytes.TrimSpace(v)) == "null" }

func equalsJSON(want string) func(json.RawMessage) bool {
	return func(v json.RawMessage) bool {
		t := string(bytes.TrimSpace(v))
		return t == "null" || t == want
	}
}

func emptyJSON(v json.RawMessage) bool {
	t := string(bytes.TrimSpace(v))
	return t == "null" || t == "{}" || t == "[]" || t == `""`
}

var chatToAnthropicPolicy = fieldPolicy{
	handled: set("model", "messages", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stop",
		"stream", "stream_options", "tools", "tool_choice", "parallel_tool_calls", "user", "reasoning_effort"),
	neutral: map[string]func(json.RawMessage) bool{
		"n": equalsJSON("1"), "presence_penalty": equalsJSON("0"), "frequency_penalty": equalsJSON("0"),
		"logprobs": equalsJSON("false"), "store": equalsJSON("false"), "metadata": emptyJSON,
		"response_format": func(v json.RawMessage) bool {
			var rf struct{ Type string }
			return isNull(v) || (json.Unmarshal(v, &rf) == nil && rf.Type == "text")
		},
	},
	hints: set("service_tier", "prompt_cache_key", "safety_identifier"),
}

var anthropicToChatPolicy = fieldPolicy{
	handled: set("model", "messages", "system", "max_tokens", "temperature", "top_p", "stop_sequences",
		"stream", "tools", "tool_choice", "metadata", "thinking", "output_config"),
	// Anthropic server-side features with no OpenAI equivalent that don't change
	// what the model is asked to do: dropped with a warning even in strict mode.
	hints: set("service_tier", "context_management", "safeguards"),
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// ChatToAnthropicRequest converts an OpenAI Chat request into an Anthropic
// Messages request for upstreamModel. Returns the body and compat warnings.
func ChatToAnthropicRequest(body []byte, upstreamModel string, compat Compat) ([]byte, []string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, invalid("请求体不是合法的 JSON：%v", err)
	}
	var is issues
	chatToAnthropicPolicy.check(raw, &is)
	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, invalid("请求格式错误：%v", err)
	}
	if len(req.Messages) == 0 {
		return nil, nil, invalid("messages 不能为空")
	}

	out := AnthropicRequest{Model: upstreamModel, Stream: req.Stream, Temperature: req.Temperature, TopP: req.TopP}
	switch {
	case req.MaxCompletionTokens != nil:
		out.MaxTokens = *req.MaxCompletionTokens
	case req.MaxTokens != nil:
		out.MaxTokens = *req.MaxTokens
	default:
		out.MaxTokens = DefaultAnthropicMaxTokens
	}
	if len(req.Stop) > 0 && !isNull(req.Stop) {
		var one string
		if json.Unmarshal(req.Stop, &one) == nil {
			out.StopSequences = []string{one}
		} else if err := json.Unmarshal(req.Stop, &out.StopSequences); err != nil {
			return nil, nil, invalid("stop 必须是字符串或字符串数组")
		}
	}
	if req.User != "" {
		out.Metadata = &AnthropicMetadata{UserID: req.User}
	}

	var system []AnthropicBlock
	var msgs []AnthropicMessage
	appendBlocks := func(role string, blocks []AnthropicBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			var prev []AnthropicBlock
			_ = json.Unmarshal(msgs[n-1].Content, &prev)
			blocks = append(prev, blocks...)
			msgs[n-1].Content, _ = json.Marshal(blocks)
			return
		}
		c, _ := json.Marshal(blocks)
		msgs = append(msgs, AnthropicMessage{Role: role, Content: c})
	}

	for i, m := range req.Messages {
		if m.Name != "" {
			is.drop("messages[].name")
		}
		switch m.Role {
		case "system", "developer":
			text, err := chatContentText(m.Content, &is)
			if err != nil {
				return nil, nil, invalid("messages[%d]：%v", i, err)
			}
			if text != "" {
				system = append(system, AnthropicBlock{Type: "text", Text: text})
			}
		case "user":
			blocks, err := chatPartsToBlocks(m.Content, &is)
			if err != nil {
				return nil, nil, invalid("messages[%d]：%v", i, err)
			}
			appendBlocks("user", blocks)
		case "assistant":
			var blocks []AnthropicBlock
			if m.reasoningText() != "" {
				// Anthropic thinking blocks need a provider signature we can't produce.
				is.drop("messages[].reasoning_content")
			}
			text, err := chatContentText(m.Content, &is)
			if err != nil {
				return nil, nil, invalid("messages[%d]：%v", i, err)
			}
			if text != "" {
				blocks = append(blocks, AnthropicBlock{Type: "text", Text: text})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Function.Arguments)
				if strings.TrimSpace(tc.Function.Arguments) == "" {
					input = json.RawMessage("{}")
				} else if !json.Valid(input) {
					return nil, nil, invalid("messages[%d].tool_calls 的 arguments 不是合法 JSON", i)
				}
				blocks = append(blocks, AnthropicBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: input})
			}
			appendBlocks("assistant", blocks)
		case "tool":
			text, err := chatContentText(m.Content, &is)
			if err != nil {
				return nil, nil, invalid("messages[%d]：%v", i, err)
			}
			content, _ := json.Marshal(text)
			appendBlocks("user", []AnthropicBlock{{Type: "tool_result", ToolUseID: m.ToolCallID, Content: content}})
		default:
			return nil, nil, &ConvertError{Class: "unsupported_parameter", Message: fmt.Sprintf("不支持的消息角色 %q", m.Role), Fields: []string{"messages[].role"}}
		}
	}
	if len(msgs) == 0 {
		return nil, nil, invalid("messages 中至少需要一条 user/assistant 消息")
	}
	out.Messages = msgs
	if len(system) > 0 {
		out.System, _ = json.Marshal(system)
	}

	if err := applyReasoningEffort(&out, req.ReasoningEffort, &is); err != nil {
		return nil, nil, err
	}

	for _, t := range req.Tools {
		if t.Type != "function" {
			is.unsupportedField("tools[].type=" + t.Type)
			continue
		}
		if t.Function.Strict != nil {
			is.drop("tools[].function.strict")
		}
		schema := t.Function.Parameters
		if isNull(schema) {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, AnthropicTool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: schema})
	}
	if len(req.ToolChoice) > 0 && !isNull(req.ToolChoice) {
		var s string
		var obj struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		switch {
		case json.Unmarshal(req.ToolChoice, &s) == nil:
			switch s {
			case "auto":
				out.ToolChoice = &AnthropicChoice{Type: "auto"}
			case "none":
				out.ToolChoice = &AnthropicChoice{Type: "none"}
			case "required":
				out.ToolChoice = &AnthropicChoice{Type: "any"}
			default:
				return nil, nil, invalid("不支持的 tool_choice %q", s)
			}
		case json.Unmarshal(req.ToolChoice, &obj) == nil && obj.Type == "function":
			out.ToolChoice = &AnthropicChoice{Type: "tool", Name: obj.Function.Name}
		default:
			is.unsupportedField("tool_choice")
		}
	}
	if req.ParallelToolCalls != nil && !*req.ParallelToolCalls && len(out.Tools) > 0 {
		if out.ToolChoice == nil {
			out.ToolChoice = &AnthropicChoice{Type: "auto"}
		}
		t := true
		out.ToolChoice.DisableParallelToolUse = &t
	}

	warnings, err := is.resolve(compat, Anthropic)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(out)
	return b, warnings, err
}

// DefaultAnthropicMaxTokens is used when an OpenAI request omits max_tokens
// (Anthropic requires it).
const DefaultAnthropicMaxTokens = 4096

// chatContentText flattens string or text-part content to a string.
func chatContentText(raw json.RawMessage, is *issues) (string, error) {
	if isNull(raw) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []ChatPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("content 必须是字符串或内容数组")
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text", "refusal":
			b.WriteString(p.Text)
		default:
			is.unsupportedField("content[].type=" + p.Type)
		}
	}
	return b.String(), nil
}

func chatPartsToBlocks(raw json.RawMessage, is *issues) ([]AnthropicBlock, error) {
	if isNull(raw) {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return []AnthropicBlock{{Type: "text", Text: s}}, nil
	}
	var parts []ChatPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("content 必须是字符串或内容数组")
	}
	var out []AnthropicBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, AnthropicBlock{Type: "text", Text: p.Text})
		case "image_url":
			if p.ImageURL == nil || p.ImageURL.URL == "" {
				return nil, fmt.Errorf("image_url 缺少 url")
			}
			if p.ImageURL.Detail != "" && p.ImageURL.Detail != "auto" {
				is.drop("image_url.detail")
			}
			src, err := imageSourceFromURL(p.ImageURL.URL)
			if err != nil {
				return nil, err
			}
			out = append(out, AnthropicBlock{Type: "image", Source: src})
		default:
			is.unsupportedField("content[].type=" + p.Type)
		}
	}
	return out, nil
}

func imageSourceFromURL(u string) (*AnthropicSource, error) {
	if rest, ok := strings.CutPrefix(u, "data:"); ok {
		meta, data, ok := strings.Cut(rest, ",")
		mediaType, isB64 := strings.CutSuffix(meta, ";base64")
		if !ok || !isB64 || mediaType == "" {
			return nil, fmt.Errorf("图片 data URL 必须是 base64 编码")
		}
		return &AnthropicSource{Type: "base64", MediaType: mediaType, Data: data}, nil
	}
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return &AnthropicSource{Type: "url", URL: u}, nil
	}
	return nil, fmt.Errorf("不支持的图片地址")
}

// AnthropicToChatRequest converts an Anthropic Messages request into an OpenAI
// Chat request. maxTokensField selects "max_tokens" or "max_completion_tokens".
func AnthropicToChatRequest(body []byte, upstreamModel, maxTokensField string, compat Compat) ([]byte, []string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, invalid("请求体不是合法的 JSON：%v", err)
	}
	var is issues
	anthropicToChatPolicy.check(raw, &is)
	var req AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, invalid("请求格式错误：%v", err)
	}
	if len(req.Messages) == 0 {
		return nil, nil, invalid("messages 不能为空")
	}

	out := ChatRequest{Model: upstreamModel, Stream: req.Stream, Temperature: req.Temperature, TopP: req.TopP}
	mt := req.MaxTokens
	if maxTokensField == "max_completion_tokens" {
		out.MaxCompletionTokens = &mt
	} else {
		out.MaxTokens = &mt
	}
	if req.Stream {
		out.StreamOptions = &StreamOptions{IncludeUsage: true}
	}
	if len(req.StopSequences) > 0 {
		out.Stop, _ = json.Marshal(req.StopSequences)
	}
	if req.Metadata != nil {
		out.User = pseudonymousUser(req.Metadata.UserID)
	}
	effort := ""
	if oc := req.OutputConfig; oc != nil {
		for k, v := range oc {
			if k != "effort" {
				is.unsupportedField("output_config." + k)
				continue
			}
			_ = json.Unmarshal(v, &effort)
			switch effort {
			case "low", "medium", "high":
			case "max":
				effort = "high"
			default:
				return nil, nil, invalid("不支持的 output_config.effort %q", effort)
			}
		}
	}
	thinkingType := ""
	if req.Thinking != nil {
		thinkingType = req.Thinking.Type
	}
	switch thinkingType {
	case "enabled":
		out.ReasoningEffort = effortForBudget(req.Thinking.BudgetTokens)
		if effort != "" {
			out.ReasoningEffort = effort
		}
	case "adaptive":
		out.ReasoningEffort = "medium"
		if effort != "" {
			out.ReasoningEffort = effort
		}
	case "", "disabled":
		if thinkingType == "" && effort != "" {
			out.ReasoningEffort = effort
		}
	default:
		is.unsupportedField("thinking.type=" + thinkingType)
	}

	if !isNull(req.System) {
		var s string
		if json.Unmarshal(req.System, &s) == nil {
			if s != "" {
				out.Messages = append(out.Messages, chatText("system", s))
			}
		} else {
			var blocks []AnthropicBlock
			if err := json.Unmarshal(req.System, &blocks); err != nil {
				return nil, nil, invalid("system 必须是字符串或文本块数组")
			}
			var b strings.Builder
			for _, bl := range blocks {
				if len(bl.CacheControl) > 0 {
					is.drop("cache_control")
				}
				if bl.Type != "text" {
					is.unsupportedField("system[].type=" + bl.Type)
					continue
				}
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString(bl.Text)
			}
			if b.Len() > 0 {
				out.Messages = append(out.Messages, chatText("system", b.String()))
			}
		}
	}

	for i, m := range req.Messages {
		blocks, err := anthropicBlocks(m.Content)
		if err != nil {
			return nil, nil, invalid("messages[%d]：%v", i, err)
		}
		switch m.Role {
		case "user":
			var parts []ChatPart
			for _, bl := range blocks {
				if len(bl.CacheControl) > 0 {
					is.drop("cache_control")
				}
				switch bl.Type {
				case "text":
					parts = append(parts, ChatPart{Type: "text", Text: bl.Text})
				case "image":
					url, err := imageURLFromSource(bl.Source)
					if err != nil {
						return nil, nil, invalid("messages[%d]：%v", i, err)
					}
					parts = append(parts, ChatPart{Type: "image_url", ImageURL: &ChatImageURL{URL: url}})
				case "tool_result":
					text, err := toolResultText(bl, &is)
					if err != nil {
						return nil, nil, invalid("messages[%d]：%v", i, err)
					}
					content, _ := json.Marshal(text)
					out.Messages = append(out.Messages, ChatMessage{Role: "tool", ToolCallID: bl.ToolUseID, Content: content})
				default:
					is.unsupportedField("content[].type=" + bl.Type)
				}
			}
			if len(parts) > 0 {
				out.Messages = append(out.Messages, chatParts("user", parts))
			}
		case "assistant":
			msg := ChatMessage{Role: "assistant"}
			var text strings.Builder
			for _, bl := range blocks {
				if len(bl.CacheControl) > 0 {
					is.drop("cache_control")
				}
				switch bl.Type {
				case "text":
					text.WriteString(bl.Text)
				case "tool_use":
					args := string(bl.Input)
					if isNull(bl.Input) {
						args = "{}"
					}
					msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{ID: bl.ID, Type: "function", Function: ChatToolCallFunc{Name: bl.Name, Arguments: args}})
				case "thinking", "redacted_thinking":
					is.drop("thinking")
				default:
					is.unsupportedField("content[].type=" + bl.Type)
				}
			}
			if text.Len() > 0 || len(msg.ToolCalls) == 0 {
				msg.Content, _ = json.Marshal(text.String())
			}
			out.Messages = append(out.Messages, msg)
		case "system":
			// Newer Anthropic clients (e.g. Claude Code) may place system messages
			// inside the conversation; OpenAI accepts system messages anywhere.
			var b strings.Builder
			for _, bl := range blocks {
				if len(bl.CacheControl) > 0 {
					is.drop("cache_control")
				}
				if bl.Type != "text" {
					is.unsupportedField("content[].type=" + bl.Type)
					continue
				}
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString(bl.Text)
			}
			if b.Len() > 0 {
				out.Messages = append(out.Messages, chatText("system", b.String()))
			}
		default:
			return nil, nil, invalid("不支持的消息角色 %q", m.Role)
		}
	}

	for _, t := range req.Tools {
		if len(t.CacheControl) > 0 {
			is.drop("cache_control")
		}
		if t.Type != "" && t.Type != "custom" {
			is.unsupportedField("tools[].type=" + t.Type)
			continue
		}
		schema := t.InputSchema
		if isNull(schema) {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, ChatTool{Type: "function", Function: ChatFunction{Name: t.Name, Description: t.Description, Parameters: schema}})
	}
	if req.ToolChoice != nil {
		switch req.ToolChoice.Type {
		case "auto":
			out.ToolChoice = json.RawMessage(`"auto"`)
		case "any":
			out.ToolChoice = json.RawMessage(`"required"`)
		case "none":
			out.ToolChoice = json.RawMessage(`"none"`)
		case "tool":
			out.ToolChoice, _ = json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": req.ToolChoice.Name}})
		default:
			is.unsupportedField("tool_choice")
		}
		if d := req.ToolChoice.DisableParallelToolUse; d != nil && *d && len(out.Tools) > 0 {
			f := false
			out.ParallelToolCalls = &f
		}
	}

	warnings, err := is.resolve(compat, OpenAIChat)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(out)
	return b, warnings, err
}

func chatText(role, s string) ChatMessage {
	c, _ := json.Marshal(s)
	return ChatMessage{Role: role, Content: c}
}

func chatParts(role string, parts []ChatPart) ChatMessage {
	if len(parts) == 1 && parts[0].Type == "text" {
		return chatText(role, parts[0].Text)
	}
	c, _ := json.Marshal(parts)
	return ChatMessage{Role: role, Content: c}
}

func anthropicBlocks(raw json.RawMessage) ([]AnthropicBlock, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []AnthropicBlock{{Type: "text", Text: s}}, nil
	}
	var blocks []AnthropicBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("content 必须是字符串或内容块数组")
	}
	return blocks, nil
}

func imageURLFromSource(src *AnthropicSource) (string, error) {
	if src == nil {
		return "", fmt.Errorf("image 缺少 source")
	}
	switch src.Type {
	case "base64":
		return "data:" + src.MediaType + ";base64," + src.Data, nil
	case "url":
		return src.URL, nil
	default:
		return "", fmt.Errorf("不支持的图片来源类型 %q", src.Type)
	}
}

func toolResultText(bl AnthropicBlock, is *issues) (string, error) {
	var text string
	if !isNull(bl.Content) {
		var s string
		if json.Unmarshal(bl.Content, &s) == nil {
			text = s
		} else {
			var blocks []AnthropicBlock
			if err := json.Unmarshal(bl.Content, &blocks); err != nil {
				return "", fmt.Errorf("tool_result.content 格式错误")
			}
			var b strings.Builder
			for _, c := range blocks {
				if c.Type == "text" {
					b.WriteString(c.Text)
				} else {
					is.unsupportedField("tool_result.content[].type=" + c.Type)
				}
			}
			text = b.String()
		}
	}
	if bl.IsError {
		is.drop("tool_result.is_error")
		text = "[tool error] " + text
	}
	return text, nil
}

// Reasoning ("thinking") mapping between OpenAI reasoning_effort and Anthropic
// extended thinking budgets. The mapping is approximate by nature, so the
// chosen budget/effort is deterministic and documented here:
//
//	effort  minimal/none -> thinking disabled
//	        low -> 2048, medium -> 8192, high -> 24576 budget tokens
//	budget  <4096 -> low, <16384 -> medium, otherwise high
var effortBudgets = map[string]int64{"low": 2048, "medium": 8192, "high": 24576}

func effortForBudget(budget int64) string {
	switch {
	case budget < 4096:
		return "low"
	case budget < 16384:
		return "medium"
	default:
		return "high"
	}
}

// applyReasoningEffort enables Anthropic extended thinking for an OpenAI
// reasoning_effort. Anthropic requires max_tokens > budget and does not accept
// a custom temperature/top_p with thinking, so those are adjusted and reported.
func applyReasoningEffort(out *AnthropicRequest, effort string, is *issues) error {
	switch effort {
	case "", "none", "minimal":
		return nil
	}
	budget, ok := effortBudgets[effort]
	if !ok {
		return invalid("不支持的 reasoning_effort %q", effort)
	}
	out.Thinking = &AnthropicThinking{Type: "enabled", BudgetTokens: budget}
	if out.MaxTokens <= budget {
		out.MaxTokens = budget + out.MaxTokens
		is.drop("max_tokens(raised_for_thinking)")
	}
	if out.Temperature != nil && *out.Temperature != 1 {
		out.Temperature = nil
		is.drop("temperature(thinking)")
	}
	if out.TopP != nil {
		out.TopP = nil
		is.drop("top_p(thinking)")
	}
	return nil
}

// pseudonymousUser keeps short end-user ids as-is and replaces long ones (such
// as Claude Code's JSON-encoded device/session metadata) with a stable SHA-256
// pseudonym, so upstream length limits aren't hit and raw device ids aren't
// forwarded to third parties.
func pseudonymousUser(id string) string {
	if len(id) <= 64 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "og-" + hex.EncodeToString(sum[:16])
}
