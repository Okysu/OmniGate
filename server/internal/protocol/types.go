package protocol

import "encoding/json"

// Dialect names used for inbound endpoints and upstream channels.
const (
	OpenAIChat      = "openai.chat"
	OpenAIResponses = "openai.responses"
	Anthropic       = "anthropic.messages"
	OpenAIModels    = "openai.models"
	// OpenAIEmbeddings is passthrough-only (OpenAI channels).
	OpenAIEmbeddings = "openai.embeddings"
	// Image endpoints (docs/contracts/phase7-api.md §1) are passthrough-only
	// (OpenAI channels); OpenAIImages is the plaza protocol covering all three.
	OpenAIImages            = "openai.images"
	OpenAIImagesGenerations = "openai.images.generations"
	OpenAIImagesEdits       = "openai.images.edits"
	OpenAIImagesVariations  = "openai.images.variations"
)

// IsImages reports whether dialect is one of the image endpoints.
func IsImages(dialect string) bool {
	return dialect == OpenAIImagesGenerations || dialect == OpenAIImagesEdits || dialect == OpenAIImagesVariations
}

// OpenAIOnly reports whether dialect is served by OpenAI-compatible channels
// only, without conversion (embeddings, images, audio).
func OpenAIOnly(dialect string) bool {
	return dialect == OpenAIEmbeddings || IsImages(dialect) || IsAudio(dialect)
}

// ---- OpenAI Chat Completions ----

type ChatRequest struct {
	Model               string          `json:"model"`
	Messages            []ChatMessage   `json:"messages"`
	MaxTokens           *int64          `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int64          `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"` // string | []string
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *StreamOptions  `json:"stream_options,omitempty"`
	Tools               []ChatTool      `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	User                string          `json:"user,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	// PromptCacheKey and SafetyIdentifier exist in Chat and Responses alike
	// and are carried across that conversion (phase12-api.md §6): upstream
	// prompt caches and account pools recognise sessions by them.
	PromptCacheKey   string `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier string `json:"safety_identifier,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"` // string | []ChatPart | null
	Name       string          `json:"name,omitempty"`
	ToolCalls  []ChatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	// ReasoningContent is a widely used non-standard field (DeepSeek, vLLM...);
	// some providers use "reasoning" instead (or send both with the same text).
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
}

// reasoningText prefers reasoning_content and falls back to reasoning.
func (m *ChatMessage) reasoningText() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	return m.Reasoning
}

type ChatPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ChatImageURL `json:"image_url,omitempty"`
}

type ChatImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type ChatTool struct {
	Type     string       `json:"type"`
	Function ChatFunction `json:"function"`
}

type ChatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type ChatToolCall struct {
	Index    *int             `json:"index,omitempty"` // streaming only
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ChatToolCallFunc `json:"function"`
}

type ChatToolCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type ChatChoice struct {
	Index        int          `json:"index"`
	Message      *ChatMessage `json:"message,omitempty"`
	Delta        *ChatDelta   `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason"`
}

// reasoningText prefers reasoning_content and falls back to reasoning.
func (d *ChatDelta) reasoningText() string {
	if d.ReasoningContent != nil && *d.ReasoningContent != "" {
		return *d.ReasoningContent
	}
	if d.Reasoning != nil {
		return *d.Reasoning
	}
	return ""
}

type ChatDelta struct {
	Role             string         `json:"role,omitempty"`
	Content          *string        `json:"content,omitempty"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	Reasoning        *string        `json:"reasoning,omitempty"`
	ToolCalls        []ChatToolCall `json:"tool_calls,omitempty"`
	Refusal          *string        `json:"refusal,omitempty"`
}

// ---- Anthropic Messages ----

type AnthropicRequest struct {
	Model         string                     `json:"model"`
	Messages      []AnthropicMessage         `json:"messages"`
	System        json.RawMessage            `json:"system,omitempty"` // string | []block
	MaxTokens     int64                      `json:"max_tokens"`
	Temperature   *float64                   `json:"temperature,omitempty"`
	TopP          *float64                   `json:"top_p,omitempty"`
	StopSequences []string                   `json:"stop_sequences,omitempty"`
	Stream        bool                       `json:"stream,omitempty"`
	Tools         []AnthropicTool            `json:"tools,omitempty"`
	ToolChoice    *AnthropicChoice           `json:"tool_choice,omitempty"`
	Metadata      *AnthropicMetadata         `json:"metadata,omitempty"`
	Thinking      *AnthropicThinking         `json:"thinking,omitempty"`
	OutputConfig  map[string]json.RawMessage `json:"output_config,omitempty"`
}

type AnthropicThinking struct {
	Type         string `json:"type"` // enabled | disabled | adaptive
	BudgetTokens int64  `json:"budget_tokens,omitempty"`
}

type AnthropicMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

type AnthropicChoice struct {
	Type                   string `json:"type"` // auto | any | tool | none
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
}

type AnthropicTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	Type         string          `json:"type,omitempty"` // server tools carry a type
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type AnthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string | []AnthropicBlock
}

type AnthropicBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image / document
	Source *AnthropicSource `json:"source,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string | []block
	IsError   bool            `json:"is_error,omitempty"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// cache_control is a hint we may need to drop
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type AnthropicSource struct {
	Type      string `json:"type"` // base64 | url
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type AnthropicResponse struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Model        string           `json:"model"`
	Content      []AnthropicBlock `json:"content"`
	StopReason   *string          `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        anthropicUsage   `json:"usage"`
}
