package pricing

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"omnigate/internal/money"
	"omnigate/internal/protocol"
)

// Context-length tiers (phase10-api.md §1): a price version may carry up to
// MaxTiers tiers, each applying to the whole request once its prompt tokens
// exceed the tier's threshold — like OpenAI / Gemini long-context pricing.

// MaxTiers is the maximum number of tiers of a price version.
const MaxTiers = 5

// MaxTierThreshold bounds aboveInputTokens.
const MaxTierThreshold = 1_000_000_000

// Tier is one context-length tier. InputPerM and OutputPerM are always set;
// the optional prices are nil when the tier inherits the base version's field
// verbatim (so a nil ImageInputPM inherits the base's ImageInputPM, which may
// itself be nil = billed at the tier's input price).
type Tier struct {
	AboveInputTokens int64
	InputPerM        money.Amount
	OutputPerM       money.Amount
	CacheReadPM      *money.Amount
	CacheWritePM     *money.Amount
	ImageInputPM     *money.Amount
	AudioInputPM     *money.Amount
	AudioOutputPM    *money.Amount
}

// TierInput is the JSON form of a tier: request bodies, responses, the stored
// document and the seed catalog. Optional prices: omitted / null / "" =
// inherit the base version's field.
type TierInput struct {
	AboveInputTokens int64   `json:"aboveInputTokens"`
	InputPerM        string  `json:"inputPerM"`
	OutputPerM       string  `json:"outputPerM"`
	CacheReadPerM    *string `json:"cacheReadPerM"`
	CacheWritePerM   *string `json:"cacheWritePerM"`
	ImageInputPerM   *string `json:"imageInputPerM"`
	AudioInputPerM   *string `json:"audioInputPerM"`
	AudioOutputPerM  *string `json:"audioOutputPerM"`
}

// Input renders t in its JSON form (amounts as canonical decimal strings).
func (t Tier) Input() TierInput {
	return TierInput{AboveInputTokens: t.AboveInputTokens, InputPerM: t.InputPerM.String(), OutputPerM: t.OutputPerM.String(),
		CacheReadPerM: optionalString(t.CacheReadPM), CacheWritePerM: optionalString(t.CacheWritePM),
		ImageInputPerM: optionalString(t.ImageInputPM), AudioInputPerM: optionalString(t.AudioInputPM),
		AudioOutputPerM: optionalString(t.AudioOutputPM)}
}

// TierInputs renders tiers in their JSON form (nil when there are none).
func TierInputs(tiers []Tier) []TierInput {
	if len(tiers) == 0 {
		return nil
	}
	out := make([]TierInput, len(tiers))
	for i, t := range tiers {
		out[i] = t.Input()
	}
	return out
}

// compileTiers validates tiers. Field problems are added to details under
// "tiers[i].<field>", list problems under "tiers". An empty list compiles to
// nil (no tiers).
func compileTiers(in []TierInput, details map[string]any) []Tier {
	if len(in) == 0 {
		return nil
	}
	if len(in) > MaxTiers {
		details["tiers"] = fmt.Sprintf("最多 %d 档", MaxTiers)
		return nil
	}
	out := make([]Tier, len(in))
	for i, t := range in {
		at := fmt.Sprintf("tiers[%d].", i)
		if t.AboveInputTokens < 1 || t.AboveInputTokens > MaxTierThreshold {
			details[at+"aboveInputTokens"] = fmt.Sprintf("必须是 1–%d 之间的整数", MaxTierThreshold)
		} else if i > 0 && t.AboveInputTokens <= in[i-1].AboveInputTokens {
			details[at+"aboveInputTokens"] = "必须大于上一档的 aboveInputTokens（按升序排列，不能重复）"
		}
		required := func(s, field string) money.Amount {
			if s == "" {
				details[at+field] = "必填"
				return 0
			}
			return parseAmount(s, at+field, details)
		}
		optional := func(s *string, field string) *money.Amount {
			if s == nil || *s == "" {
				return nil
			}
			a := parseAmount(*s, at+field, details)
			return &a
		}
		out[i] = Tier{AboveInputTokens: t.AboveInputTokens,
			InputPerM: required(t.InputPerM, "inputPerM"), OutputPerM: required(t.OutputPerM, "outputPerM"),
			CacheReadPM: optional(t.CacheReadPerM, "cacheReadPerM"), CacheWritePM: optional(t.CacheWritePerM, "cacheWritePerM"),
			ImageInputPM: optional(t.ImageInputPerM, "imageInputPerM"), AudioInputPM: optional(t.AudioInputPerM, "audioInputPerM"),
			AudioOutputPM: optional(t.AudioOutputPerM, "audioOutputPerM")}
	}
	return out
}

// encodeTiers is the stored document (nil = SQL NULL).
func encodeTiers(tiers []Tier) any {
	if len(tiers) == 0 {
		return nil
	}
	b, _ := json.Marshal(TierInputs(tiers))
	return b
}

// decodeTiers parses a stored document (invalid documents yield none; they
// cannot be written).
func decodeTiers(raw []byte) []Tier {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var in []TierInput
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	details := map[string]any{}
	out := compileTiers(in, details)
	if len(details) > 0 {
		return nil
	}
	return out
}

func sameTiers(a, b []Tier) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.AboveInputTokens != y.AboveInputTokens || x.InputPerM != y.InputPerM || x.OutputPerM != y.OutputPerM ||
			!sameOptional(x.CacheReadPM, y.CacheReadPM) || !sameOptional(x.CacheWritePM, y.CacheWritePM) ||
			!sameOptional(x.ImageInputPM, y.ImageInputPM) || !sameOptional(x.AudioInputPM, y.AudioInputPM) ||
			!sameOptional(x.AudioOutputPM, y.AudioOutputPM) {
			return false
		}
	}
	return true
}

// PromptTokens is the context size that selects a tier: input + cache read +
// cache write tokens (Usage.Input excludes cached tokens; image and audio
// input tokens are part of Input). Negative counts are 0; the sum saturates.
func PromptTokens(u protocol.Usage) int64 {
	var n int64
	for _, v := range []int64{u.Input, u.CacheRead, u.CacheWrite} {
		v = max(v, 0)
		if n > math.MaxInt64-v {
			return math.MaxInt64
		}
		n += v
	}
	return n
}

// TierFor returns the tier applying to a request with `prompt` prompt tokens:
// the last tier whose threshold is exceeded (prompt > aboveInputTokens), nil
// for the base prices.
func (p *Price) TierFor(prompt int64) *Tier {
	if p == nil {
		return nil
	}
	var out *Tier
	for i := range p.Tiers {
		if prompt > p.Tiers[i].AboveInputTokens {
			out = &p.Tiers[i]
		}
	}
	return out
}

// AppliedTier is the threshold of the tier that prices usage u (nil = base
// prices): the request log's priceTier.
func (p *Price) AppliedTier(u protocol.Usage) *int64 {
	t := p.TierFor(PromptTokens(u))
	if t == nil {
		return nil
	}
	v := t.AboveInputTokens
	return &v
}

// WithTier returns the version's unit prices with tier t applied (t's set
// fields replace the base's; the per-request, per-image, per-minute and
// per-character prices are never tiered). nil t returns p. The result has no
// tiers.
func (p *Price) WithTier(t *Tier) *Price {
	if t == nil {
		return p
	}
	q := *p
	q.Tiers = nil
	q.InputPerM, q.OutputPerM = t.InputPerM, t.OutputPerM
	if t.CacheReadPM != nil {
		q.CacheReadPM = *t.CacheReadPM
	}
	if t.CacheWritePM != nil {
		q.CacheWritePM = *t.CacheWritePM
	}
	if t.ImageInputPM != nil {
		q.ImageInputPM = t.ImageInputPM
	}
	if t.AudioInputPM != nil {
		q.AudioInputPM = t.AudioInputPM
	}
	if t.AudioOutputPM != nil {
		q.AudioOutputPM = t.AudioOutputPM
	}
	return &q
}

// FormatTokens renders a token threshold compactly with at most one
// decimal: 272000 → "272K", 1000000 → "1M", 1500000 → "1.5M"; other values
// in full.
func FormatTokens(n int64) string {
	switch {
	case n >= 1_000_000 && n%100_000 == 0:
		return strconv.FormatFloat(float64(n)/1e6, 'f', -1, 64) + "M"
	case n >= 1_000 && n%100 == 0:
		return strconv.FormatFloat(float64(n)/1e3, 'f', -1, 64) + "K"
	}
	return strconv.FormatInt(n, 10)
}
