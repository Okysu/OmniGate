package protocol

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Reasoning continuity across dialects.
//
// Providers only accept their own reasoning back: Anthropic thinking blocks
// carry a signature, Responses reasoning items carry encrypted_content, and
// neither is valid at the other. To keep a model's reasoning across turns of
// a converted conversation, the provider blob travels to the client inside
// the client dialect's own opaque field (Anthropic thinking.signature,
// Responses reasoning.encrypted_content), wrapped in an envelope that records
// whose blob it is. When the client sends its history back, the blob is
// unwrapped and given only to the provider that produced it; other providers
// get the reasoning text where they can take it (Chat reasoning_content on
// tool-call turns) or nothing.
//
// Inside the Chat pivot the blocks travel in the private x_og_reasoning field,
// which never reaches a Chat client or a Chat upstream.

// Reasoning block providers.
const (
	ReasoningOpenAI    = "openai"    // Responses encrypted_content
	ReasoningAnthropic = "anthropic" // thinking signature / redacted_thinking data
	ReasoningText      = "text"      // plain text, no provider blob (Chat upstreams)
)

// ReasoningBlock is one reasoning item / thinking block in the pivot.
type ReasoningBlock struct {
	Provider string `json:"p,omitempty"`
	ID       string `json:"id,omitempty"` // Responses reasoning item id
	Text     string `json:"t,omitempty"`  // summary / thinking text (not in envelopes)
	Blob     string `json:"v,omitempty"`  // encrypted_content / signature / redacted data
	Redacted bool   `json:"r,omitempty"`  // Anthropic redacted_thinking
}

const envelopePrefix = "og1."

// envelope wraps a block's provider blob for a client of another dialect.
func (b ReasoningBlock) envelope() string {
	e := b
	e.Text = ""
	if e.Provider == "" {
		e.Provider = ReasoningText
	}
	j, _ := json.Marshal(e)
	return envelopePrefix + base64.RawURLEncoding.EncodeToString(j)
}

// openEnvelope reads a blob a client sent back: an OmniGate envelope, or a
// raw provider blob, attributed to fallback (the provider native to the
// client dialect, whose history may come from an unconverted channel).
func openEnvelope(s, fallback string) (ReasoningBlock, bool) {
	if s == "" {
		return ReasoningBlock{}, false
	}
	if rest, ok := strings.CutPrefix(s, envelopePrefix); ok {
		raw, err := base64.RawURLEncoding.DecodeString(rest)
		var b ReasoningBlock
		if err != nil || json.Unmarshal(raw, &b) != nil {
			return ReasoningBlock{}, false
		}
		return b, true
	}
	return ReasoningBlock{Provider: fallback, Blob: s}, true
}

// reasoningBlocksText joins the text of reasoning blocks.
func reasoningBlocksText(bs []ReasoningBlock) string {
	var parts []string
	for _, b := range bs {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// IsReasoningVerificationError reports whether an upstream error rejects
// reasoning handed back in the history (Responses encrypted_content or an
// Anthropic thinking signature that cannot be verified, e.g. made by another
// account of a pool). The request itself is fine without that reasoning.
func IsReasoningVerificationError(raw []byte) bool {
	m := strings.ToLower(string(raw))
	return strings.Contains(m, "encrypted content") && (strings.Contains(m, "could not be verified") || strings.Contains(m, "could not be decrypted")) ||
		strings.Contains(m, "signature") && strings.Contains(m, "thinking") && strings.Contains(m, "invalid")
}

// StripReasoning removes the reasoning handed back in a converted upstream
// request (Responses reasoning items, Anthropic thinking blocks), for a retry
// after IsReasoningVerificationError: the model then reasons afresh.
func StripReasoning(upstreamDialect string, body []byte) []byte {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return body
	}
	switch upstreamDialect {
	case OpenAIResponses:
		var items []map[string]json.RawMessage
		if json.Unmarshal(raw["input"], &items) != nil {
			return body
		}
		kept := items[:0]
		for _, it := range items {
			var typ string
			_ = json.Unmarshal(it["type"], &typ)
			if typ != "reasoning" {
				kept = append(kept, it)
			}
		}
		raw["input"], _ = json.Marshal(kept)
	case Anthropic:
		var msgs []map[string]json.RawMessage
		if json.Unmarshal(raw["messages"], &msgs) != nil {
			return body
		}
		for i, m := range msgs {
			var blocks []map[string]json.RawMessage
			if json.Unmarshal(m["content"], &blocks) != nil {
				continue
			}
			kept := blocks[:0]
			for _, bl := range blocks {
				var typ string
				_ = json.Unmarshal(bl["type"], &typ)
				if typ != "thinking" && typ != "redacted_thinking" {
					kept = append(kept, bl)
				}
			}
			msgs[i]["content"], _ = json.Marshal(kept)
		}
		raw["messages"], _ = json.Marshal(msgs)
	default:
		return body
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

// hasEnvelope reports whether a body may contain an OmniGate envelope.
func hasEnvelope(body []byte) bool { return strings.Contains(string(body), envelopePrefix) }

// UnwrapReasoningForPassthrough prepares a client's history for a native
// channel of its own dialect: envelopes holding that provider's blob are
// unwrapped, other converted reasoning is removed. Used when a conversation
// that went through conversion continues on a passthrough channel.
func UnwrapReasoningForPassthrough(dialect string, body []byte) []byte {
	if !hasEnvelope(body) {
		return body
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return body
	}
	changed := false
	switch dialect {
	case Anthropic:
		var msgs []map[string]json.RawMessage
		if json.Unmarshal(raw["messages"], &msgs) != nil {
			return body
		}
		for i, m := range msgs {
			var blocks []map[string]json.RawMessage
			if json.Unmarshal(m["content"], &blocks) != nil {
				continue
			}
			kept := blocks[:0]
			touched := false
			for _, bl := range blocks {
				var typ string
				_ = json.Unmarshal(bl["type"], &typ)
				field := map[string]string{"thinking": "signature", "redacted_thinking": "data"}[typ]
				if field == "" {
					kept = append(kept, bl)
					continue
				}
				var v string
				_ = json.Unmarshal(bl[field], &v)
				if !strings.HasPrefix(v, envelopePrefix) {
					kept = append(kept, bl)
					continue
				}
				touched = true
				if b, ok := openEnvelope(v, Anthropic); ok && b.Provider == ReasoningAnthropic && b.Blob != "" {
					bl[field], _ = json.Marshal(b.Blob)
					kept = append(kept, bl)
				}
			}
			if touched {
				changed = true
				msgs[i]["content"], _ = json.Marshal(kept)
			}
		}
		if changed {
			raw["messages"], _ = json.Marshal(msgs)
		}
	case OpenAIResponses:
		var items []map[string]json.RawMessage
		if json.Unmarshal(raw["input"], &items) != nil {
			return body
		}
		kept := items[:0]
		for _, it := range items {
			var typ, enc string
			_ = json.Unmarshal(it["type"], &typ)
			_ = json.Unmarshal(it["encrypted_content"], &enc)
			if typ != "reasoning" || !strings.HasPrefix(enc, envelopePrefix) {
				kept = append(kept, it)
				continue
			}
			changed = true
			if b, ok := openEnvelope(enc, ReasoningOpenAI); ok && b.Provider == ReasoningOpenAI && b.Blob != "" {
				it["encrypted_content"], _ = json.Marshal(b.Blob)
				delete(it, "id") // the client's id is ours, not the upstream's
				kept = append(kept, it)
			}
		}
		if changed {
			raw["input"], _ = json.Marshal(kept)
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}
