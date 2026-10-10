package affinity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/channel"
)

// Outcomes recorded in request_logs.affinity (phase12-api.md §4).
const (
	OutcomeHit          = "hit"           // served by the bound channel
	OutcomeNew          = "new"           // no binding yet: the serving channel was bound
	OutcomeMiss         = "miss"          // no binding and the request failed (nothing bound)
	OutcomeRebound      = "rebound"       // another channel served; the binding moved to it (switch_on_success)
	OutcomeFailover     = "failover"      // the bound channel did not serve; the binding was kept
	OutcomeBroken       = "broken"        // the bound channel was not a candidate (disabled, circuit open, no longer allowed)
	OutcomeStrictFailed = "strict_failed" // strict: the bound channel failed and no other channel was tried
	OutcomeOff          = "off"           // the rule applied with session_mode off (headers passed, no pinning)
)

// Outcomes lists every outcome (request log filter values).
var Outcomes = []string{OutcomeHit, OutcomeNew, OutcomeMiss, OutcomeRebound, OutcomeFailover, OutcomeBroken, OutcomeStrictFailed, OutcomeOff}

// Service matches requests against the current setting and keeps the
// bindings of this instance.
type Service struct {
	store  *Store
	config func(context.Context) Config
}

// NewService creates the service; config returns the effective setting and
// now is the clock (nil = time.Now).
func NewService(config func(context.Context) Config, now func() time.Time) *Service {
	return &Service{store: NewStore(now), config: config}
}

// Stats describes the binding store.
type Stats struct {
	Entries    int            `json:"entries"`
	MaxEntries int            `json:"maxEntries"`
	Rules      map[string]int `json:"rules"`
}

// Stats returns the live binding counts and the configured capacity.
func (s *Service) Stats(ctx context.Context) Stats {
	n, per := s.store.Stats()
	return Stats{Entries: n, MaxEntries: s.config(ctx).MaxEntries, Rules: per}
}

// Clear removes all bindings (rule == "") or those of one rule.
func (s *Service) Clear(rule string) int { return s.store.Clear(rule) }

// Begin matches req and returns the request's affinity session (nil when no
// rule applies). The current binding is looked up (and refreshed) here.
func (s *Service) Begin(ctx context.Context, req Request) *Session {
	if s == nil {
		return nil
	}
	cfg := s.config(ctx)
	m := cfg.Match(req)
	if m == nil {
		return nil
	}
	ttl := m.Rule.TTLSeconds
	if ttl == 0 {
		ttl = cfg.DefaultTTLSeconds
	}
	ss := &Session{store: s.store, Rule: m.Rule.Name, Mode: m.Rule.Mode(cfg.SessionMode), pass: m.Rule.Pass(),
		key: m.bindingKey(req), ttl: time.Duration(ttl) * time.Second, capacity: cfg.MaxEntries,
		switchOnSuccess: cfg.SwitchOnSuccess, keepOnDisabled: cfg.KeepOnChannelDisabled}
	if m.Rule.InjectPromptCacheKey || m.Rule.InjectSessionHeader != "" {
		id := upstreamID(ss.key)
		if m.Rule.InjectPromptCacheKey {
			ss.cacheKey = "og-" + hex.EncodeToString(id[:16])
		}
		if m.Rule.InjectSessionHeader != "" {
			ss.header = [2]string{m.Rule.InjectSessionHeader, uuidV8(id)}
		}
	}
	if ss.Mode != ModeOff {
		ss.bound, ss.hasBound = s.store.Get(ss.key)
	}
	return ss
}

// upstreamID derives the identity sent upstream from the binding key (the
// same material: user, include_* parts, session value), domain-separated so
// the in-memory key itself never leaves the process and the raw session
// value is never sent.
func upstreamID(key [32]byte) [32]byte {
	return sha256.Sum256(append([]byte("omnigate-affinity/upstream\x00"), key[:]...))
}

// uuidV8 formats the first 16 bytes of id as an RFC 9562 UUIDv8 (version
// nibble 8, RFC 4122 variant), lowercase 8-4-4-4-12.
func uuidV8(id [32]byte) string {
	b := id[:16]
	b[6] = b[6]&0x0f | 0x80
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Session is one request's affinity state. A nil *Session is valid: every
// method is a no-op (no rule applied).
type Session struct {
	store *Store
	// Rule is the applying rule's name; Mode its effective session mode.
	Rule string
	Mode string

	pass                            *channel.PassHeaders
	cacheKey                        string    // inject_prompt_cache_key ("" = off)
	header                          [2]string // inject_session_header: name, value ("" = off)
	key                             [32]byte
	ttl                             time.Duration
	capacity                        int
	switchOnSuccess, keepOnDisabled bool

	bound     uuid.UUID
	hasBound  bool
	routed    bool // the bound channel was a candidate and put first
	broken    bool
	strict    bool // strict stop after the bound channel failed
	attempted bool // an upstream attempt was made
	served    *uuid.UUID
}

// PassHeaders returns the client headers to copy upstream (nil = none).
func (s *Session) PassHeaders() *channel.PassHeaders {
	if s == nil {
		return nil
	}
	return s.pass
}

// PromptCacheKey returns the prompt_cache_key to add to OpenAI-format
// upstream bodies without one ("" = none): "og-" and 32 hex characters.
func (s *Session) PromptCacheKey() string {
	if s == nil {
		return ""
	}
	return s.cacheKey
}

// SessionHeader returns the header to add to OpenAI-format upstream requests
// that do not carry it yet, with its UUID value (name "" = none).
func (s *Session) SessionHeader() (name, value string) {
	if s == nil {
		return "", ""
	}
	return s.header[0], s.header[1]
}

// Bound returns the channel the session is bound to, if it pins at all.
func (s *Session) Bound() (uuid.UUID, bool) {
	if s == nil || s.Mode == ModeOff || !s.hasBound {
		return uuid.Nil, false
	}
	return s.bound, true
}

// Routed records that the bound channel is a candidate and goes first.
func (s *Session) Routed() {
	if s != nil {
		s.routed = true
	}
}

// Attempted records that the request was sent to an upstream channel.
// Requests rejected before any attempt (balance, quota, limits) get no
// outcome beyond off / broken.
func (s *Session) Attempted() {
	if s != nil {
		s.attempted = true
	}
}

// Broken records that the bound channel is not a candidate: the binding is
// dropped unless keep_on_channel_disabled (then it also survives the request
// being served elsewhere, so the session returns once the channel is back).
func (s *Session) Broken() {
	if s == nil || !s.hasBound {
		return
	}
	s.broken = true
	if !s.keepOnDisabled {
		s.store.Delete(s.key)
	}
}

// StopAfter reports whether a failed attempt on channel id must end the
// request without failover: strict mode and id is the bound channel the
// request was routed to.
func (s *Session) StopAfter(id uuid.UUID) bool {
	if s == nil || s.Mode != ModeStrict || !s.routed || id != s.bound {
		return false
	}
	s.strict = true
	return true
}

// Served records the channel that served the request. primary is false when
// a route rule's fallback model served it: fallback models are never pinned
// and the binding is left as it is.
func (s *Session) Served(id uuid.UUID, primary bool) {
	if s == nil || s.Mode == ModeOff || !primary {
		return
	}
	s.served = &id
	switch {
	case s.broken && s.keepOnDisabled:
		// Kept for the disabled channel's return.
	case s.hasBound && !s.broken && id != s.bound && !s.switchOnSuccess:
		// Served elsewhere; the old binding stays.
	default:
		s.store.Set(s.key, id, s.Rule, s.ttl, s.capacity)
	}
}

// Outcome is the value for request_logs.affinity ("" = no rule applied, or
// the request never reached an upstream).
func (s *Session) Outcome() string {
	switch {
	case s == nil:
		return ""
	case s.Mode == ModeOff:
		return OutcomeOff
	case s.strict:
		return OutcomeStrictFailed
	case s.broken:
		return OutcomeBroken
	case !s.attempted:
		return ""
	case !s.hasBound && s.served != nil:
		return OutcomeNew
	case !s.hasBound:
		return OutcomeMiss
	case s.served != nil && *s.served == s.bound:
		return OutcomeHit
	case s.served != nil && s.switchOnSuccess:
		return OutcomeRebound
	}
	return OutcomeFailover
}
