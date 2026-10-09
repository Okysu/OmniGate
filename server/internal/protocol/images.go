package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Image endpoints (docs/contracts/phase7-api.md §1): requests are passed
// through with only the model rewritten; responses are forwarded unchanged and
// metered by the number of output images and the gpt-image-* usage object.

// MaxImageCount bounds the requested image count used for balance holds.
const MaxImageCount = 100

// ImageCount clamps a requested n to [1, MaxImageCount] (absent or invalid = 1).
func ImageCount(n int64) int64 {
	if n < 1 {
		return 1
	}
	return min(n, MaxImageCount)
}

// ParseImageInfo extracts routing information from a JSON image request
// (generations, or edits sent as JSON): model, stream, n and the prompt size.
func ParseImageInfo(body []byte) (RequestInfo, error) {
	var r struct {
		Model  string          `json:"model"`
		Stream bool            `json:"stream"`
		N      json.RawMessage `json:"n"`
		Prompt json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return RequestInfo{}, invalid("请求体不是合法的 JSON：%v", err)
	}
	if strings.TrimSpace(r.Model) == "" {
		return RequestInfo{}, invalid("缺少 model 字段")
	}
	info := RequestInfo{Model: r.Model, Stream: r.Stream, BodyBytes: len(body), PromptBytes: len(r.Prompt)}
	var n int64
	if len(r.N) > 0 && json.Unmarshal(r.N, &n) == nil {
		info.Images = n
	}
	info.Images = ImageCount(info.Images)
	return info, nil
}

// imagesPassthrough forwards image SSE events unchanged
// (image_generation.partial_image / image_generation.completed /
// image_edit.*) and meters the completed events.
type imagesPassthrough struct {
	usage    Usage
	hasUsage bool
	images   int64
	done     bool
}

// NewImageStreamProcessor returns the stream processor of the image endpoints.
func NewImageStreamProcessor() StreamProcessor { return &imagesPassthrough{} }

func (p *imagesPassthrough) Process(ev *Event) ([]byte, error) {
	data := bytes.TrimSpace(ev.Data)
	if bytes.Equal(data, doneData) {
		p.done = true
		return ev.Raw, nil
	}
	var e struct {
		Type  string      `json:"type"`
		Usage *imageUsage `json:"usage"`
	}
	if len(data) == 0 || json.Unmarshal(data, &e) != nil {
		return ev.Raw, nil
	}
	typ := e.Type
	if typ == "" {
		typ = ev.Name
	}
	switch {
	case strings.HasSuffix(typ, ".completed"):
		p.images++
		p.done = true
		if e.Usage != nil {
			// With n > 1 every completed event may carry usage; whether it is per
			// image or cumulative is not specified, so the largest value of each
			// field is kept (never more than the upstream reported once).
			u := e.Usage.normalize()
			p.usage.Input = max(p.usage.Input, u.Input)
			p.usage.Output = max(p.usage.Output, u.Output)
			p.usage.ImageInput = max(p.usage.ImageInput, u.ImageInput)
			p.hasUsage = true
		}
	case typ == "error":
		p.done = true // terminal: the error reached the client in-band
	}
	return ev.Raw, nil
}

func (p *imagesPassthrough) Finish() []byte { return nil }

// Usage returns the metered usage; Images is always set, ok reports whether
// the upstream sent token usage.
func (p *imagesPassthrough) Usage() (Usage, bool) {
	u := p.usage
	u.Images = p.images
	return u, p.hasUsage
}

func (p *imagesPassthrough) OutputBytes() int { return 0 }
func (p *imagesPassthrough) Done() bool       { return p.done }
