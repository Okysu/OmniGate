package affinity

import (
	"context"
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
	if ss.Mode != ModeOff {
		ss.bound, ss.hasBound = s.store.Get(ss.key)
	}
	return ss
}

// Session is one request's affinity state. A nil *Session is valid: every
// method is a no-op (no rule applied).
type Session struct {
	store *Store
	// Rule is the applying rule's name; Mode its effective session mode.
	Rule string
	Mode string

	pass                            *channel.PassHeaders
	key                             [32]byte
	ttl                             time.Duration
	capacity                        int
	switchOnSuccess, keepOnDisabled bool

	bound    uuid.UUID
	hasBound bool
	routed   bool // the bound channel was a candidate and put first
	broken   bool
	strict   bool // strict stop after the bound channel failed
	served   *uuid.UUID
}

// PassHeaders returns the client headers to copy upstream (nil = none).
func (s *Session) PassHeaders() *channel.PassHeaders {
	if s == nil {
		return nil
	}
	return s.pass
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

// Outcome is the value for request_logs.affinity ("" = no rule applied).
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
