package protocol

import (
	"bytes"
	"encoding/json"
)

// Legacy text completions (/v1/completions, phase14-api.md) are passthrough
// only: the body is forwarded with the upstream model (and, when streaming,
// stream_options.include_usage like Chat), and the response is metered here.

// completionUsage is the usage object of a completions response: the Chat
// Completions fields plus DeepSeek's prompt cache counters
// (prompt_cache_hit_tokens + prompt_cache_miss_tokens = prompt_tokens).
type completionUsage struct {
	chatUsage
	PromptCacheHitTokens  *int64 `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens *int64 `json:"prompt_cache_miss_tokens,omitempty"`
}

// normalize maps the usage like chatUsage (cache reads are separate from
// Input): prompt_tokens_details.cached_tokens wins; without it DeepSeek's
// prompt_cache_hit_tokens are the cache reads and Input is the rest of
// prompt_tokens (prompt_cache_miss_tokens when prompt_tokens is missing).
func (c completionUsage) normalize() Usage {
	u := c.chatUsage.normalize()
	if c.PromptTokensDetails != nil || c.PromptCacheHitTokens == nil {
		return u
	}
	u.CacheRead = max(*c.PromptCacheHitTokens, 0)
	switch {
	case c.PromptTokens > 0:
		u.Input = max(c.PromptTokens-u.CacheRead, 0)
	case c.PromptCacheMissTokens != nil:
		u.Input = max(*c.PromptCacheMissTokens, 0)
	}
	return u
}

// UsageFromCompletionResponse extracts usage from a non-streaming completions
// response body.
func UsageFromCompletionResponse(body []byte) (Usage, bool) {
	var r struct {
		Usage *completionUsage `json:"usage"`
	}
	if json.Unmarshal(body, &r) != nil || r.Usage == nil {
		return Usage{}, false
	}
	return r.Usage.normalize(), true
}

// completionChunk is one text_completion stream chunk.
type completionChunk struct {
	Choices []struct {
		Text string `json:"text"`
	} `json:"choices"`
	Usage *completionUsage `json:"usage"`
}

// completionPassthrough relays a completions stream unchanged and meters it
// like chatPassthrough: the usage chunk the gateway forced with include_usage
// is hidden from clients that didn't ask for it.
type completionPassthrough struct {
	includeUsage bool
	usage        Usage
	hasUsage     bool
	out          int
	done         bool
}

func (p *completionPassthrough) Process(ev *Event) ([]byte, error) {
	if bytes.Equal(bytes.TrimSpace(ev.Data), doneData) {
		p.done = true
		return ev.Raw, nil
	}
	var chunk completionChunk
	if len(ev.Data) > 0 && json.Unmarshal(ev.Data, &chunk) == nil {
		for _, c := range chunk.Choices {
			p.out += len(c.Text)
		}
		if chunk.Usage != nil {
			p.usage, p.hasUsage = chunk.Usage.normalize(), true
			if !p.includeUsage && len(chunk.Choices) == 0 {
				return nil, nil
			}
		}
	}
	return ev.Raw, nil
}

func (p *completionPassthrough) Finish() []byte       { return nil }
func (p *completionPassthrough) Usage() (Usage, bool) { return p.usage, p.hasUsage }
func (p *completionPassthrough) OutputBytes() int     { return p.out }
func (p *completionPassthrough) Done() bool           { return p.done }
