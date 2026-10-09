package channel

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Breaker is an in-memory per-channel circuit breaker driven by real traffic
// (passive health) and explicit tests. healthy → degraded (1..threshold-1
// consecutive failures) → open (cooldown) → half-open (one probe request).
type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	now       func() time.Time
	states    map[uuid.UUID]*breakerState

	// OnTransition, when set, is called (outside the lock) when a channel's
	// circuit opens (unhealthy = true, with the last error) and when an open
	// circuit recovers (unhealthy = false). It must not block.
	OnTransition func(id uuid.UUID, unhealthy bool, lastErr string)
}

type breakerState struct {
	fails     int
	openUntil time.Time
	probing   bool
	lastErr   string
	lastAt    time.Time
}

func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown, now: time.Now, states: map[uuid.UUID]*breakerState{}}
}

func (b *Breaker) state(id uuid.UUID) *breakerState {
	s, ok := b.states[id]
	if !ok {
		s = &breakerState{}
		b.states[id] = s
	}
	return s
}

// Allow reports whether a request may be sent to the channel. When the
// cooldown has elapsed exactly one probe is let through.
func (b *Breaker) Allow(id uuid.UUID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(id)
	if s.fails < b.threshold {
		return true
	}
	if b.now().Before(s.openUntil) || s.probing {
		return false
	}
	s.probing = true
	return true
}

// Success resets the channel to healthy.
func (b *Breaker) Success(id uuid.UUID) {
	b.mu.Lock()
	s := b.state(id)
	recovered := s.fails >= b.threshold
	*s = breakerState{lastAt: b.now()}
	b.mu.Unlock()
	if recovered && b.OnTransition != nil {
		b.OnTransition(id, false, "")
	}
}

// Failure records a health-relevant failure (connection error, timeout, 401/403, 429, 5xx).
func (b *Breaker) Failure(id uuid.UUID, msg string) {
	b.mu.Lock()
	s := b.state(id)
	s.fails++
	s.lastErr, s.lastAt, s.probing = msg, b.now(), false
	opened := s.fails == b.threshold
	if s.fails >= b.threshold {
		s.openUntil = b.now().Add(b.cooldown)
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
	h := Health{ConsecutiveFailures: s.fails}
	switch {
	case s.fails == 0:
		h.State = "healthy"
	case s.fails < b.threshold:
		h.State = "degraded"
	default:
		h.State = "open"
	}
	if s.lastErr != "" {
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
	case !ok || s.fails < b.threshold:
		return "closed"
	case b.now().Before(s.openUntil):
		return "open"
	default:
		return "half_open"
	}
}
