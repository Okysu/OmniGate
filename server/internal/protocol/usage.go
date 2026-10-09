package protocol

import "encoding/json"

// Usage is normalized token usage. Input excludes cache reads/writes so that
// each dimension is priced exactly once; Output includes reasoning tokens
// (both OpenAI and Anthropic bill reasoning as output), Reasoning is informative.
//
// Image endpoints (phase7-api.md §1.1, §4.8): Input counts every input token
// (text and image), ImageInput is the image part of it; Images is the number
// of output images.
//
// Audio endpoints (phase9-api.md §1.1): AudioInput / AudioOutput are the audio
// parts of Input / Output; AudioSeconds is the billed duration of the input
// audio (whole seconds, rounded up); Characters is the number of input
// characters of a speech request billed by characters. Estimated on an audio
// request means the upstream reported no usage: only perRequest is charged.
type Usage struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cacheRead"`
	CacheWrite   int64 `json:"cacheWrite"`
	Reasoning    int64 `json:"reasoning"`
	Estimated    bool  `json:"estimated"`
	ImageInput   int64 `json:"imageInputTokens,omitempty"`
	Images       int64 `json:"images,omitempty"`
	AudioInput   int64 `json:"audioInputTokens,omitempty"`
	AudioOutput  int64 `json:"audioOutputTokens,omitempty"`
	AudioSeconds int64 `json:"audioSeconds,omitempty"`
	Characters   int64 `json:"characters,omitempty"`
}

func (u Usage) IsZero() bool {
	return u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.CacheWrite == 0 && u.Images == 0 &&
		u.AudioSeconds == 0 && u.Characters == 0
}

// chatUsage is the OpenAI Chat Completions usage object.
type chatUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

func (c chatUsage) normalize() Usage {
	u := Usage{Input: c.PromptTokens, Output: c.CompletionTokens}
	if c.PromptTokensDetails != nil {
		u.CacheRead = c.PromptTokensDetails.CachedTokens
		u.Input -= u.CacheRead
	}
	if c.CompletionTokensDetails != nil {
		u.Reasoning = c.CompletionTokensDetails.ReasoningTokens
	}
	if u.Input < 0 {
		u.Input = 0
	}
	return u
}

func chatUsageFrom(u Usage) *chatUsage {
	c := &chatUsage{
		PromptTokens:     u.Input + u.CacheRead + u.CacheWrite,
		CompletionTokens: u.Output,
	}
	c.TotalTokens = c.PromptTokens + c.CompletionTokens
	if u.CacheRead > 0 {
		c.PromptTokensDetails = &struct {
			CachedTokens int64 `json:"cached_tokens"`
		}{u.CacheRead}
	}
	if u.Reasoning > 0 {
		c.CompletionTokensDetails = &struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		}{u.Reasoning}
	}
	return c
}

// anthropicUsage is the Anthropic Messages usage object.
type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
}

func (a anthropicUsage) normalize() Usage {
	return Usage{Input: a.InputTokens, Output: a.OutputTokens, CacheRead: a.CacheReadInputTokens, CacheWrite: a.CacheCreationInputTokens}
}

func anthropicUsageFrom(u Usage) anthropicUsage {
	return anthropicUsage{InputTokens: u.Input, OutputTokens: u.Output, CacheReadInputTokens: u.CacheRead, CacheCreationInputTokens: u.CacheWrite}
}

// responsesUsage is the OpenAI Responses usage object.
type responsesUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details,omitempty"`
}

func (r responsesUsage) normalize() Usage {
	u := Usage{Input: r.InputTokens, Output: r.OutputTokens}
	if r.InputTokensDetails != nil {
		u.CacheRead = r.InputTokensDetails.CachedTokens
		u.Input -= u.CacheRead
	}
	if r.OutputTokensDetails != nil {
		u.Reasoning = r.OutputTokensDetails.ReasoningTokens
	}
	if u.Input < 0 {
		u.Input = 0
	}
	return u
}

// UsageFromChatResponse extracts usage from a non-streaming Chat response body.
func UsageFromChatResponse(body []byte) (Usage, bool) {
	var r struct {
		Usage *chatUsage `json:"usage"`
	}
	if json.Unmarshal(body, &r) != nil || r.Usage == nil {
		return Usage{}, false
	}
	return r.Usage.normalize(), true
}

// UsageFromAnthropicResponse extracts usage from a non-streaming Messages response.
func UsageFromAnthropicResponse(body []byte) (Usage, bool) {
	var r struct {
		Usage *anthropicUsage `json:"usage"`
	}
	if json.Unmarshal(body, &r) != nil || r.Usage == nil {
		return Usage{}, false
	}
	return r.Usage.normalize(), true
}

// UsageFromResponsesResponse extracts usage from a non-streaming Responses body.
func UsageFromResponsesResponse(body []byte) (Usage, bool) {
	var r struct {
		Usage *responsesUsage `json:"usage"`
	}
	if json.Unmarshal(body, &r) != nil || r.Usage == nil {
		return Usage{}, false
	}
	return r.Usage.normalize(), true
}

// EstimateTokens is a deliberately simple estimator (≈4 bytes per token) used
// only when the upstream returns no usage; results are flagged Estimated.
func EstimateTokens(n int) int64 {
	if n <= 0 {
		return 0
	}
	return int64((n + 3) / 4)
}

// imageUsage is the usage object of the OpenAI image endpoints (gpt-image-*).
type imageUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails *struct {
		TextTokens  int64 `json:"text_tokens"`
		ImageTokens int64 `json:"image_tokens"`
	} `json:"input_tokens_details,omitempty"`
}

func (r imageUsage) normalize() Usage {
	u := Usage{Input: r.InputTokens, Output: r.OutputTokens}
	if r.InputTokensDetails != nil {
		u.ImageInput = min(max(r.InputTokensDetails.ImageTokens, 0), max(u.Input, 0))
	}
	return u
}

// UsageFromImagesResponse extracts usage from a non-streaming image response:
// Images is the length of data; ok reports whether token usage was present
// (dall-e-* report none: such requests are billed per image only).
func UsageFromImagesResponse(body []byte) (u Usage, ok bool, err error) {
	var r struct {
		Data  []json.RawMessage `json:"data"`
		Usage *imageUsage       `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Usage{}, false, err
	}
	if r.Usage != nil {
		u, ok = r.Usage.normalize(), true
	}
	u.Images = int64(len(r.Data))
	return u, ok, nil
}
