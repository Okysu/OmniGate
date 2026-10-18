package channel

import (
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Breaker is an in-memory circuit breaker per channel and per channel × model,
// driven by real traffic (passive health), explicit tests and the recovery
// prober (docs/adr/0011-circuit-breaker.md).
//
// Failures are classified (FailKind): connection errors and 5xx count against
// the channel, response-header timeouts at half weight, 401 opens the channel
// with a long cooldown, 403 and other model-specific failures only affect the
// model on that channel, and rate limits (429, exhausted quota) never open a
// circuit — they pause the model on the channel for its Retry-After.
//
// A circuit opens when, within the window, failures span at least
// MinFailSeconds distinct seconds (a burst of concurrent requests failing
// together during one upstream restart counts once) and make up at least
// FailRatio of the requests. An open circuit cools down for an adaptive time
// (BaseCooldown doubling per failed probe up to MaxCooldown), then lets up to
// HalfOpenProbes requests through at once; a success closes it.
//
// The gateway never fails a request just because circuits are open: when
// every candidate is blocked it forces a "last resort" attempt through
// ForceAllow (at most MaxForced at a time per circuit), and when even that is
// impossible it answers 503 with the RetryAt of the soonest circuit.
type Breaker struct {
	mu       sync.Mutex
	cfg      BreakerConfig
	now      func() time.Time
	channels map[uuid.UUID]*circuit
	models   map[Target]*circuit
	alwaysOn map[uuid.UUID]bool // channels exempt from blocking

	// OnTransition, when set, is called (outside the lock) when a channel's
	// circuit opens (unhealthy = true, with the last error) and when an open
	// channel circuit recovers (unhealthy = false). It must not block.
	OnTransition func(id uuid.UUID, unhealthy bool, lastErr string)
}

// BreakerConfig tunes the breaker; DefaultBreakerConfig is used in production.
type BreakerConfig struct {
	Window         time.Duration // failures are counted within this window
	Bucket         time.Duration // failures within one bucket count once (1 s)
	NoMerge        bool          // tests: every outcome gets its own bucket
	MinFailSeconds float64       // weighted distinct failure seconds needed to open
	FailRatio      float64       // and at least this share of the requests
	BaseCooldown   time.Duration // first cooldown; doubled per failed probe
	MaxCooldown    time.Duration
	AuthCooldown   time.Duration // first cooldown of a 401-opened channel
	MaxAuthCool    time.Duration
	HalfOpenProbes int           // concurrent probes once the cooldown elapsed
	MaxForced      int           // concurrent last-resort attempts per circuit
	DefaultPause   time.Duration // rate-limit pause without Retry-After
	MaxPause       time.Duration
}

// DefaultBreakerConfig is the production tuning.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		Window: time.Minute, Bucket: time.Second, MinFailSeconds: 3, FailRatio: 0.5,
		BaseCooldown: 5 * time.Second, MaxCooldown: time.Minute,
		AuthCooldown: time.Minute, MaxAuthCool: 10 * time.Minute,
		HalfOpenProbes: 3, MaxForced: 3,
		DefaultPause: 10 * time.Second, MaxPause: 5 * time.Minute,
	}
}

// Target is what a request touches: a channel, and the model it is serving
// there ("" = the channel as a whole, e.g. connectivity tests).
type Target struct {
	Channel uuid.UUID
	Model   string
}

// FailKind classifies a health-relevant failure.
type FailKind int

const (
	// FailTransient: connection errors, 5xx, malformed responses (channel).
	FailTransient FailKind = iota
	// FailTimeout: no response headers in time (channel, half weight).
	FailTimeout
	// FailAuth: 401, the channel's credentials (channel, long cooldown).
	FailAuth
	// FailModel: 403 and other failures specific to one model (model scope).
	FailModel
	// FailRateLimit: 429 or exhausted upstream quota — pauses the model on
	// the channel for the Retry-After, never opens a circuit.
	FailRateLimit
)

// StatusFailKind maps the status of a failed upstream check to its kind;
// ok=false when the status says nothing about the channel's health (404 or
// 405 from an upstream without the endpoint, other 4xx).
func StatusFailKind(status int) (kind FailKind, ok bool) {
	switch {
	case status == 401:
		return FailAuth, true
	case status == 403:
		return FailModel, true
	case status == 408:
		return FailTimeout, true
	case status == 429:
		return FailRateLimit, true
	case status >= 500:
		return FailTransient, true
	}
	return 0, false
}

// Reasons of an open circuit (Health.Reason).
const (
	ReasonFailures  = "failures"
	ReasonAuth      = "auth"
	ReasonRateLimit = "rate_limit"
)

type bucket struct {
	sec   int64
	req   int
	fail  int
	failW float64
}

type circuit struct {
	buckets     []bucket // one per second within the window, oldest first
	consecutive int
	open        bool
	reason      string
	openUntil   time.Time
	level       int // cooldown doubling; reset by a real request's success
	inflight    int // probes / last-resort attempts let through while blocked
	pausedUntil time.Time
	lastErr     string
	lastAt      time.Time
	rejected    int64
}

// NewBreaker returns a breaker with cfg.
func NewBreaker(cfg BreakerConfig) *Breaker {
	if cfg.HalfOpenProbes < 1 {
		cfg.HalfOpenProbes = 1
	}
	if cfg.Bucket <= 0 {
		cfg.Bucket = time.Second
	}
	return &Breaker{cfg: cfg, now: time.Now, channels: map[uuid.UUID]*circuit{}, models: map[Target]*circuit{}}
}

func (b *Breaker) channel(id uuid.UUID) *circuit {
	c, ok := b.channels[id]
	if !ok {
		c = &circuit{}
		b.channels[id] = c
	}
	return c
}

func (b *Breaker) model(t Target) *circuit {
	if t.Model == "" {
		return nil
	}
	c, ok := b.models[t]
	if !ok {
		c = &circuit{}
		b.models[t] = c
	}
	return c
}

// scopes returns the circuits of t: the channel's, then the model's (if any).
func (b *Breaker) scopes(t Target) []*circuit {
	out := []*circuit{b.channel(t.Channel)}
	if m := b.model(t); m != nil {
		out = append(out, m)
	}
	return out
}

func (b *Breaker) slot(t time.Time) int64 { return t.UnixNano() / int64(b.cfg.Bucket) }

func (b *Breaker) prune(c *circuit, now time.Time) {
	cut := b.slot(now.Add(-b.cfg.Window))
	i := 0
	for i < len(c.buckets) && c.buckets[i].sec <= cut {
		i++
	}
	if i > 0 {
		c.buckets = append(c.buckets[:0], c.buckets[i:]...)
	}
}

func (b *Breaker) record(c *circuit, now time.Time, failW float64) {
	b.prune(c, now)
	sec := b.slot(now)
	if n := len(c.buckets); n == 0 || c.buckets[n-1].sec != sec || b.cfg.NoMerge {
		c.buckets = append(c.buckets, bucket{sec: sec})
	}
	bk := &c.buckets[len(c.buckets)-1]
	bk.req++
	if failW > 0 {
		bk.fail++
		bk.failW += failW
	}
}

// stats returns the failed and total requests and the weighted distinct
// failure seconds within the window.
func (b *Breaker) stats(c *circuit, now time.Time) (fails, total int, failSeconds float64) {
	b.prune(c, now)
	for _, bk := range c.buckets {
		fails += bk.fail
		total += bk.req
		failSeconds += min(bk.failW, 1)
	}
	return fails, total, failSeconds
}

func (b *Breaker) cooldown(c *circuit) time.Duration {
	base, maxD := b.cfg.BaseCooldown, b.cfg.MaxCooldown
	if c.reason == ReasonAuth {
		base, maxD = b.cfg.AuthCooldown, b.cfg.MaxAuthCool
	}
	d := base
	for i := 0; i < c.level && d < maxD; i++ {
		d *= 2
	}
	return min(d, maxD)
}

// blocked reports whether c stops normal traffic at now, and whether a
// half-open probe may pass.
func (b *Breaker) blocked(c *circuit, now time.Time) (blocked, probe bool) {
	if c == nil {
		return false, false
	}
	if now.Before(c.pausedUntil) {
		return true, false
	}
	if !c.open {
		return false, false
	}
	if now.Before(c.openUntil) {
		return true, false
	}
	return true, c.inflight < b.cfg.HalfOpenProbes
}

// SetAlwaysOn replaces the set of channels exempt from the breaker
// (Config.AlwaysOn): their outcomes are still recorded for their health,
// but Allow, Blocked and ForceAllow never refuse them.
func (b *Breaker) SetAlwaysOn(ids map[uuid.UUID]bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.alwaysOn = ids
}

// Allow reports whether a request may be sent to t. Once an open circuit's
// cooldown has elapsed up to HalfOpenProbes requests pass at a time.
func (b *Breaker) Allow(t Target) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.alwaysOn[t.Channel] {
		return true
	}
	now := b.now()
	cs := b.scopes(t)
	var probes []*circuit
	for _, c := range cs {
		bl, probe := b.blocked(c, now)
		switch {
		case !bl:
		case probe:
			probes = append(probes, c)
		default:
			c.rejected++
			return false
		}
	}
	for _, c := range probes {
		c.inflight++
	}
	return true
}

// Blocked reports, without side effects, whether Allow would refuse t.
func (b *Breaker) Blocked(t Target) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.alwaysOn[t.Channel] {
		return false
	}
	now := b.now()
	for _, c := range b.scopes(t) {
		if bl, probe := b.blocked(c, now); bl && !probe {
			return true
		}
	}
	return false
}

// ForceAllow lets a last-resort request through to t whatever its circuits'
// state, at most MaxForced at a time per blocked circuit. The gateway uses it
// when every candidate of a request is blocked: an upstream that already
// recovered then serves the request instead of the client getting an error.
func (b *Breaker) ForceAllow(t Target) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.alwaysOn[t.Channel] {
		return true
	}
	now := b.now()
	var held []*circuit
	for _, c := range b.scopes(t) {
		if bl, _ := b.blocked(c, now); !bl {
			continue
		}
		if c.inflight >= b.cfg.MaxForced {
			c.rejected++
			return false
		}
		held = append(held, c)
	}
	for _, c := range held {
		c.inflight++
	}
	return true
}

// settle ends a request let through a blocked circuit.
func settle(c *circuit, now time.Time) {
	if c.inflight > 0 && (c.open || now.Before(c.pausedUntil)) {
		c.inflight--
	}
}

// Success records a successful request (for streams: response headers
// received). It closes t's open circuits and lifts a rate-limit pause.
func (b *Breaker) Success(t Target) {
	b.mu.Lock()
	now := b.now()
	recovered := false
	for i, c := range b.scopes(t) {
		settle(c, now)
		if c.open {
			if i == 0 {
				recovered = true
			}
			c.open, c.inflight, c.buckets = false, 0, nil
		}
		c.pausedUntil = time.Time{}
		c.level, c.consecutive, c.lastAt = 0, 0, now
		b.record(c, now, 0)
	}
	b.mu.Unlock()
	if recovered && b.OnTransition != nil {
		b.OnTransition(t.Channel, false, "")
	}
}

// ProbeSuccess records a successful out-of-band check of the channel (the
// recovery prober): it closes the channel circuit but keeps its backoff
// level, since a reachable model list does not prove requests succeed.
func (b *Breaker) ProbeSuccess(id uuid.UUID) {
	b.mu.Lock()
	now := b.now()
	c := b.channel(id)
	recovered := c.open
	if recovered {
		c.open, c.inflight, c.buckets = false, 0, nil
	}
	c.consecutive, c.lastAt = 0, now
	b.mu.Unlock()
	if recovered && b.OnTransition != nil {
		b.OnTransition(id, false, "")
	}
}

// Failure records a health-relevant failure of kind on t. retryAfter is the
// upstream's Retry-After for rate limits (0 = DefaultPause).
func (b *Breaker) Failure(t Target, kind FailKind, msg string, retryAfter time.Duration) {
	b.mu.Lock()
	now := b.now()
	ch := b.channel(t.Channel)
	m := b.model(t)
	if m == nil {
		m = ch // channel-only target: model-scoped kinds apply to the channel
	}
	for _, c := range b.scopes(t) {
		settle(c, now)
	}
	opened := false
	switch kind {
	case FailRateLimit:
		if retryAfter <= 0 {
			retryAfter = b.cfg.DefaultPause
		}
		until := now.Add(min(retryAfter, b.cfg.MaxPause))
		if until.After(m.pausedUntil) {
			m.pausedUntil = until
		}
		m.lastErr, m.lastAt = msg, now
	case FailModel:
		b.fail(m, now, 1, ReasonFailures, msg)
	case FailTimeout:
		opened = b.fail(ch, now, 0.5, ReasonFailures, msg)
	case FailAuth:
		opened = b.fail(ch, now, 1, ReasonAuth, msg)
	default:
		opened = b.fail(ch, now, 1, ReasonFailures, msg)
	}
	b.mu.Unlock()
	if opened && b.OnTransition != nil {
		b.OnTransition(t.Channel, true, msg)
	}
}

// fail records a failure on c and reports whether it opened c.
func (b *Breaker) fail(c *circuit, now time.Time, w float64, reason, msg string) bool {
	c.consecutive++
	c.lastErr, c.lastAt = msg, now
	if c.open {
		if !now.Before(c.openUntil) {
			// A failed half-open probe: back off further. Failures of
			// last-resort attempts during the cooldown change nothing.
			c.level++
			c.reason = reason
			c.openUntil = now.Add(b.cooldown(c))
		}
		return false
	}
	b.record(c, now, w)
	fails, total, secs := b.stats(c, now)
	if secs < b.cfg.MinFailSeconds || float64(fails) < b.cfg.FailRatio*float64(total) {
		return false
	}
	c.open, c.reason, c.inflight = true, reason, 0
	c.openUntil = now.Add(b.cooldown(c))
	return true
}

// Release ends a request let through a blocked circuit that produced neither
// a success nor a health failure.
func (b *Breaker) Release(t Target) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for _, c := range b.scopes(t) {
		settle(c, now)
	}
}

// RetryAt is when t's circuits next let normal traffic through (zero when
// they do now).
func (b *Breaker) RetryAt(t Target) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.alwaysOn[t.Channel] {
		return time.Time{}
	}
	now := b.now()
	var at time.Time
	for _, c := range b.scopes(t) {
		if now.Before(c.pausedUntil) && c.pausedUntil.After(at) {
			at = c.pausedUntil
		}
		if c.open && c.openUntil.After(at) {
			at = c.openUntil
		}
	}
	if !at.After(now) {
		return time.Time{}
	}
	return at
}

// State reports the channel circuit without side effects: "closed" (requests
// flow), "open" (cooling down; requests are skipped) or "half_open" (the
// cooldown elapsed; probes may pass).
func (b *Breaker) State(id uuid.UUID) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.channels[id]
	switch {
	case !ok || !c.open || b.alwaysOn[id]:
		return "closed"
	case b.now().Before(c.openUntil):
		return "open"
	default:
		return "half_open"
	}
}

// Health is the channel's health view, with its blocked or failing models.
func (b *Breaker) Health(id uuid.UUID) Health {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	h := Health{State: "healthy", Models: []ModelHealth{}, AlwaysOn: b.alwaysOn[id]}
	c, ok := b.channels[id]
	if ok {
		fails, _, _ := b.stats(c, now)
		h.ConsecutiveFailures = c.consecutive
		h.RejectedRequests = c.rejected
		switch {
		case c.open:
			h.State = "open"
			h.Reason = c.reason
			t := c.openUntil.UTC()
			h.RetryAt = &t
		case fails > 0:
			h.State = "degraded"
		}
		if c.lastErr != "" && (c.open || fails > 0) {
			e := c.lastErr
			h.LastError = &e
		}
		if !c.lastAt.IsZero() {
			t := c.lastAt.UTC()
			h.LastCheckedAt = &t
		}
	}
	for t, m := range b.models {
		if t.Channel != id {
			continue
		}
		fails, _, _ := b.stats(m, now)
		mh := ModelHealth{Model: t.Model, RejectedRequests: m.rejected}
		switch {
		case m.open:
			mh.State, mh.Reason = "open", m.reason
			at := m.openUntil.UTC()
			mh.RetryAt = &at
		case now.Before(m.pausedUntil):
			mh.State, mh.Reason = "paused", ReasonRateLimit
			at := m.pausedUntil.UTC()
			mh.RetryAt = &at
		case fails > 0:
			mh.State = "degraded"
		default:
			continue
		}
		if m.lastErr != "" {
			e := m.lastErr
			mh.LastError = &e
		}
		h.Models = append(h.Models, mh)
		h.RejectedRequests += m.rejected
		if h.State == "healthy" {
			h.State = "degraded"
		}
	}
	sort.Slice(h.Models, func(i, j int) bool { return h.Models[i].Model < h.Models[j].Model })
	return h
}
