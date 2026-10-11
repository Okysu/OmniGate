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
	// OpenAICompletions is the legacy text completions endpoint
	// (/v1/completions, including FIM via suffix; phase14-api.md). It is
	// passthrough-only and served by OpenAI channels that declare
	// supportsCompletions: suffix and token-array prompts have no Chat
	// equivalent, so it is never converted.
	OpenAICompletions = "openai.completions"
)

// IsImages reports whether dialect is one of the image endpoints.
func IsImages(dialect string) bool {
	return dialect == OpenAIImagesGenerations || dialect == OpenAIImagesEdits || dialect == OpenAIImagesVariations
}

// OpenAIOnly reports whether dialect is served by OpenAI-compatible channels
// only, without conversion (embeddings, images, audio, completions).
func OpenAIOnly(dialect string) bool {
	return dialect == OpenAIEmbeddings || IsImages(dialect) || IsAudio(dialect) || dialect == OpenAICompletions
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
	// OGKeepReasoning is a private pivot flag: the client can carry provider
	// reasoning blobs back (reasoning.go), so a Responses upstream is asked
	// for encrypted_content. Never sent to a Chat upstream.
	OGKeepReasoning bool `json:"x_og_keep_reasoning,omitempty"`
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
	// OGReasoning is the private pivot field carrying provider reasoning
	// blocks between converters (reasoning.go); never sent to a Chat client
	// or upstream.
	OGReasoning []ReasoningBlock `json:"x_og_reasoning,omitempty"`
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
	File     *ChatFile     `json:"file,omitempty"`
}

type ChatImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// UnmarshalJSON also accepts image_url as a bare URL string, which several
// OpenAI-compatible clients send.
func (u *ChatImageURL) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*u = ChatImageURL{URL: s}
		return nil
	}
	type plain ChatImageURL
	return json.Unmarshal(b, (*plain)(u))
}

// ChatFile is the Chat "file" content part (PDF and other documents).
// FileData is a data URL.
type ChatFile struct {
	FileID   string `json:"file_id,omitempty"`
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
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
	// OGReasoning carries provider reasoning blobs between composed stream
	// converters (reasoning.go); never sent to a Chat client.
	OGReasoning []ReasoningBlock `json:"x_og_reasoning,omitempty"`
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
	Title  string           `json:"title,omitempty"`
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
	// redacted_thinking
	Data string `json:"data,omitempty"`
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
