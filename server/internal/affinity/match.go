package affinity

import (
	"crypto/sha256"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// Request is what rule matching sees of a gateway request.
type Request struct {
	UserID uuid.UUID
	// GroupID is the user's user group (nil = none).
	GroupID *uuid.UUID
	// Model is the logical model requested by the client.
	Model string
	// Path is the request path after the /v1/v1 collapse ("/v1/responses").
	Path string
	// Dialect is the inbound protocol (protocol.OpenAIChat …); anchor key
	// sources read the body by it.
	Dialect   string
	UserAgent string
	Header    http.Header
	// Body is the JSON request body (nil for multipart bodies: gjson key
	// sources then find nothing).
	Body []byte
}

// Match is the rule that applies to a request and the session value found.
type Match struct {
	Rule  *Rule
	Value string
}

// Match returns the first rule that applies to req: its model, path and
// user-agent conditions match and one of its key sources yields a non-empty
// value (the first one in order, trimmed) that matches value_regex when set.
// A rule whose key is absent is skipped and matching continues with the next
// rule. nil when the setting is disabled or no rule applies.
func (c *Config) Match(req Request) *Match {
	if !c.Enabled {
		return nil
	}
	// The anchor is computed at most once per request, and only when a
	// matching rule gets to an anchor source.
	var anchor *string
	anchorOf := func() string {
		if anchor == nil {
			v := Anchor(req.Dialect, req.Body)
			anchor = &v
		}
		return *anchor
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if !r.matches(req) {
			continue
		}
		v := r.extract(req, anchorOf)
		if v == "" || r.value != nil && !r.value.MatchString(v) {
			continue
		}
		return &Match{Rule: r, Value: v}
	}
	return nil
}

func (r *Rule) matches(req Request) bool {
	if !anyMatch(r.model, req.Model) || !anyMatch(r.path, req.Path) {
		return false
	}
	if len(r.UserAgentInclude) == 0 {
		return true
	}
	ua := strings.ToLower(req.UserAgent)
	for _, s := range r.UserAgentInclude {
		if strings.Contains(ua, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// extract returns the first non-empty key source value ("" = none); anchor
// returns the request's conversation anchor.
func (r *Rule) extract(req Request, anchor func() string) string {
	for _, ks := range r.KeySources {
		var v string
		switch ks.Type {
		case SourceGJSON:
			if len(req.Body) > 0 {
				if res := gjson.GetBytes(req.Body, ks.Path); res.Exists() && res.Type != gjson.Null {
					v = res.String()
				}
			}
		case SourceHeader:
			if req.Header != nil {
				v = req.Header.Get(ks.Key)
			}
		case SourceAnchor:
			v = anchor()
		}
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func anyMatch(res []*regexp.Regexp, s string) bool {
	if len(res) == 0 {
		return true
	}
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// bindingKey hashes what identifies a session: always the user (a session
// never steers another user's routing), then the rule name, the user group
// and the logical model as the rule's include_* flags say, then the value.
func (m *Match) bindingKey(req Request) [32]byte {
	h := sha256.New()
	field := func(tag, v string) {
		h.Write([]byte(tag))
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	field("omnigate-affinity/u=", req.UserID.String())
	if m.Rule.IncludeRuleName {
		field("r=", m.Rule.Name)
	}
	if m.Rule.IncludeUsingGroup {
		g := ""
		if req.GroupID != nil {
			g = req.GroupID.String()
		}
		field("g=", g)
	}
	if m.Rule.IncludeModelName {
		field("m=", req.Model)
	}
	field("v=", m.Value)
	var k [32]byte
	copy(k[:], h.Sum(nil))
	return k
}
