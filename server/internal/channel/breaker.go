package channel

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Breaker is an in-memory per-channel circuit breaker driven by real traffic
// (passive health), explicit tests and the recovery prober.
//
// A channel's circuit opens only when failures accumulate: at least
// minFailures health-relevant failures within window that are also at least
// half of the requests finished in that window. A single failed request (for
// example one user's bad input that slipped through as a 5xx) therefore never
// takes a channel out of rotation. An open circuit cools down, then lets one
// probe through (half-open): success closes it, failure reopens it.
//
// States: healthy (no failure in the window) → degraded (failures, below the
// thresholds) → open (cooling down) → half-open (one probe).
type Breaker struct {
	mu          sync.Mutex
	minFailures int
	window      time.Duration
	cooldown    time.Duration
	now         func() time.Time
	states      map[uuid.UUID]*breakerState

	// OnTransition, when set, is called (outside the lock) when a channel's
	// circuit opens (unhealthy = true, with the last error) and when an open
	// circuit recovers (unhealthy = false). It must not block.
	OnTransition func(id uuid.UUID, unhealthy bool, lastErr string)
}

// maxBreakerEvents bounds the outcomes kept per channel within the window.
const maxBreakerEvents = 512

type breakerEvent struct {
	at time.Time
	ok bool
}

type breakerState struct {
	events      []breakerEvent // outcomes within the window, oldest first
	consecutive int            // consecutive failures (informative)
	open        bool
	openUntil   time.Time
	probing     bool
	lastErr     string
	lastAt      time.Time
}

// NewBreaker returns a breaker opening a channel after minFailures failures
// within window (and at least half of its requests there), for cooldown.
func NewBreaker(minFailures int, window, cooldown time.Duration) *Breaker {
	if minFailures < 1 {
		minFailures = 1
	}
	return &Breaker{minFailures: minFailures, window: window, cooldown: cooldown, now: time.Now, states: map[uuid.UUID]*breakerState{}}
}

func (b *Breaker) state(id uuid.UUID) *breakerState {
	s, ok := b.states[id]
	if !ok {
		s = &breakerState{}
		b.states[id] = s
	}
	return s
}

// prune drops outcomes older than the window.
func (b *Breaker) prune(s *breakerState, now time.Time) {
	cut := now.Add(-b.window)
	i := 0
	for i < len(s.events) && s.events[i].at.Before(cut) {
		i++
	}
	if i > 0 {
		s.events = append(s.events[:0], s.events[i:]...)
	}
	if over := len(s.events) - maxBreakerEvents; over > 0 {
		s.events = append(s.events[:0], s.events[over:]...)
	}
}

// counts returns the failures and all outcomes within the window.
func (b *Breaker) counts(s *breakerState, now time.Time) (fails, total int) {
	b.prune(s, now)
	for _, e := range s.events {
		if !e.ok {
			fails++
		}
	}
	return fails, len(s.events)
}

// Allow reports whether a request may be sent to the channel. When an open
// circuit's cooldown has elapsed exactly one probe is let through.
func (b *Breaker) Allow(id uuid.UUID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(id)
	if !s.open {
		return true
	}
	if b.now().Before(s.openUntil) || s.probing {
		return false
	}
	s.probing = true
	return true
}

// Success records a successful request; it closes an open circuit.
func (b *Breaker) Success(id uuid.UUID) {
	b.mu.Lock()
	s := b.state(id)
	now := b.now()
	recovered := s.open
	if recovered {
		*s = breakerState{}
	}
	s.events = append(s.events, breakerEvent{at: now, ok: true})
	b.prune(s, now)
	s.consecutive, s.probing, s.lastAt = 0, false, now
	b.mu.Unlock()
	if recovered && b.OnTransition != nil {
		b.OnTransition(id, false, "")
	}
}

// Failure records a health-relevant failure (connection error, timeout,
// 401/403, 429, 5xx). It opens the circuit once the window's thresholds are
// met; a failed half-open probe reopens it for another cooldown.
func (b *Breaker) Failure(id uuid.UUID, msg string) {
	b.mu.Lock()
	s := b.state(id)
	now := b.now()
	s.consecutive++
	s.lastErr, s.lastAt, s.probing = msg, now, false
	opened := false
	if s.open {
		s.openUntil = now.Add(b.cooldown)
	} else {
		s.events = append(s.events, breakerEvent{at: now, ok: false})
		if fails, total := b.counts(s, now); fails >= b.minFailures && fails*2 >= total {
			s.open, s.openUntil, opened = true, now.Add(b.cooldown), true
		}
	}
	b.mu.Unlock()
	if opened && b.OnTransition != nil {
		b.OnTransition(id, true, msg)
	}
}

// Release ends a probe that produced neither success nor a health failure.
func (b *Breaker) Release(id uuid.UUID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state(id).probing = false
}

func (b *Breaker) Health(id uuid.UUID) Health {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.states[id]
	if !ok {
		return Health{State: "healthy"}
	}
	fails, _ := b.counts(s, b.now())
	h := Health{ConsecutiveFailures: s.consecutive}
	switch {
	case s.open:
		h.State = "open"
	case fails > 0:
		h.State = "degraded"
	default:
		h.State = "healthy"
	}
	if s.lastErr != "" && (s.open || fails > 0) {
		e := s.lastErr
		h.LastError = &e
	}
	if !s.lastAt.IsZero() {
		t := s.lastAt.UTC()
		h.LastCheckedAt = &t
	}
	return h
}

// State reports the circuit state without side effects: "closed" (requests
// flow), "open" (cooling down; requests are skipped) or "half_open" (the
// cooldown elapsed; one probe request may pass).
func (b *Breaker) State(id uuid.UUID) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.states[id]
	switch {
	case !ok || !s.open:
		return "closed"
	case b.now().Before(s.openUntil):
		return "open"
	default:
		return "half_open"
	}
}
