package affinity

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strconv"

	"github.com/tidwall/gjson"

	"omnigate/internal/protocol"
)

// anchorUnit bounds every hashed content unit: long prompts and inline
// images cost at most this many bytes each, and the unit's full length is
// hashed too.
const anchorUnit = 4 << 10

// Anchor is the conversation fingerprint of an anchor key source (an
// OmniGate extension): a SHA-256 over the leading system / developer
// instructions and the first user message of the request body. Later turns
// of a conversation append messages, so the anchor stays the same, while
// sibling conversations differ in their first user message. dialect is the
// inbound protocol; other protocols, multipart bodies (nil) and conversations
// without a user message yield "".
//
//   - OpenAI Chat: leading messages with role system / developer, then the
//     first message with role user;
//   - OpenAI Responses: instructions, then input — a string is the first user
//     message; an array contributes its leading system / developer items and
//     the first user item;
//   - Anthropic Messages: system (string or blocks), then the first message
//     with role user.
//
// Legacy completions (openai.completions, phase14-api.md §5) deliberately
// yield "": their only content is prompt (and suffix), which for FIM code
// completion changes with every keystroke, so a prompt hash would differ on
// every request and only ever create fresh bindings. Rules for /v1/completions
// use a header or body key source instead.
//
// Each unit is hashed as tag "=" full length ":" its first 4 KiB, so the
// encoding is unambiguous. Text parts hash their text; other parts hash
// their fields except type and cache_control (which clients move between
// turns), each bounded the same way.
func Anchor(dialect string, body []byte) string {
	if len(body) == 0 {
		return ""
	}
	// One copy: gjson results are then substrings, not copies of the body.
	doc := string(body)
	a := anchorHash{h: sha256.New()}
	a.h.Write([]byte("omnigate-anchor/1\x00"))
	var user bool
	switch dialect {
	case protocol.OpenAIChat:
		user = a.messages(gjson.Get(doc, "messages"))
	case protocol.OpenAIResponses:
		if in := gjson.Get(doc, "instructions"); in.Exists() && in.Type != gjson.Null {
			a.unit("instructions", "")
			a.content(in)
		}
		switch in := gjson.Get(doc, "input"); {
		case in.Type == gjson.String:
			a.unit("role", "user")
			a.unit("text", in.Str)
			user = true
		case in.IsArray():
			user = a.messages(in)
		}
	case protocol.Anthropic:
		if sys := gjson.Get(doc, "system"); sys.Exists() && sys.Type != gjson.Null {
			a.unit("system", "")
			a.content(sys)
		}
		user = a.messages(gjson.Get(doc, "messages"))
	}
	if !user {
		return ""
	}
	return hex.EncodeToString(a.h.Sum(nil))
}

type anchorHash struct{ h hash.Hash }

// unit hashes one bounded, length-prefixed unit.
func (a anchorHash) unit(tag, v string) {
	a.h.Write([]byte(tag))
	a.h.Write([]byte("=" + strconv.Itoa(len(v)) + ":"))
	a.h.Write([]byte(v[:min(len(v), anchorUnit)]))
	a.h.Write([]byte{0})
}

// messages hashes the leading system / developer messages (items) and the
// first user message; it reports whether a user message was found. Messages
// without a role (Responses function calls, reasoning items …) end the
// leading instructions like assistant messages do.
func (a anchorHash) messages(list gjson.Result) bool {
	if !list.IsArray() {
		return false
	}
	leading, user := true, false
	list.ForEach(func(_, m gjson.Result) bool {
		switch role := m.Get("role").String(); role {
		case "system", "developer":
			if leading {
				a.unit("role", role)
				a.content(m.Get("content"))
			}
		case "user":
			a.unit("role", role)
			a.content(m.Get("content"))
			user = true
			return false
		default:
			leading = false
		}
		return true
	})
	return user
}

// content hashes a message content: a string, or an array of parts.
func (a anchorHash) content(c gjson.Result) {
	switch {
	case c.Type == gjson.String:
		a.unit("text", c.Str)
	case c.IsArray():
		c.ForEach(func(_, p gjson.Result) bool {
			a.part(p)
			return true
		})
	case c.Exists() && c.Type != gjson.Null:
		a.unit("raw", c.Raw)
	}
}

// part hashes one content part: the text of text parts (Chat text, Responses
// input_text / output_text, Anthropic text), otherwise the type and the
// part's other fields as raw JSON, each bounded.
func (a anchorHash) part(p gjson.Result) {
	if p.Type == gjson.String {
		a.unit("text", p.Str)
		return
	}
	if !p.IsObject() {
		a.unit("raw", p.Raw)
		return
	}
	typ := p.Get("type").String()
	if t := p.Get("text"); t.Type == gjson.String && (typ == "text" || typ == "input_text" || typ == "output_text") {
		a.unit("text", t.Str)
		return
	}
	a.unit("part", typ)
	p.ForEach(func(k, v gjson.Result) bool {
		if k.Str != "type" && k.Str != "cache_control" {
			a.unit("field:"+k.Str, v.Raw)
		}
		return true
	})
}
