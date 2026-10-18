package channel

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func testBreaker() (*Breaker, *time.Time) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	b := NewBreaker(DefaultBreakerConfig())
	b.now = func() time.Time { return now }
	return b, &now
}

// failSeconds records one failure in each of n distinct seconds.
func failSeconds(b *Breaker, now *time.Time, t Target, kind FailKind, n int) {
	for i := range n {
		if i > 0 {
			*now = now.Add(time.Second)
		}
		b.Failure(t, kind, "boom", 0)
	}
}

func TestBreakerOpensOnRepeatedFailures(t *testing.T) {
	b, now := testBreaker()
	id := uuid.New()
	tg := Target{Channel: id, Model: "m"}
	var transitions []bool
	b.OnTransition = func(_ uuid.UUID, unhealthy bool, _ string) { transitions = append(transitions, unhealthy) }
	failSeconds(b, now, tg, FailTransient, 2)
	if b.State(id) != "closed" || b.Health(id).State != "degraded" {
		t.Fatalf("2 failure seconds must not open: %s %+v", b.State(id), b.Health(id))
	}
	*now = now.Add(time.Second)
	b.Failure(tg, FailTransient, "boom", 0)
	if b.State(id) != "open" || b.Allow(tg) {
		t.Fatalf("3 failure seconds must open and reject: %s", b.State(id))
	}
	h := b.Health(id)
	if h.Reason != ReasonFailures || h.RetryAt == nil || !h.RetryAt.Equal(now.Add(5*time.Second)) || h.RejectedRequests != 1 {
		t.Fatalf("open health = %+v", h)
	}
	// After the 5 s cooldown up to three probes pass at once.
	*now = now.Add(5 * time.Second)
	if b.State(id) != "half_open" || !b.Allow(tg) || !b.Allow(tg) || !b.Allow(tg) || b.Allow(tg) {
		t.Fatal("half-open lets exactly three probes through")
	}
	// A failed probe doubles the cooldown; the other probes' failures do not
	// stack once it is open again.
	b.Failure(tg, FailTransient, "still down", 0)
	b.Failure(tg, FailTransient, "still down", 0)
	b.Release(tg)
	if at := b.RetryAt(tg); !at.Equal(now.Add(10 * time.Second)) {
		t.Fatalf("second cooldown until %s, want +10s", at.Sub(*now))
	}
	*now = now.Add(10 * time.Second)
	if !b.Allow(tg) {
		t.Fatal("probe after the second cooldown")
	}
	b.Success(tg)
	if b.State(id) != "closed" || b.Health(id).State != "healthy" || b.Allow(tg) != true {
		t.Fatalf("a successful probe closes the circuit: %+v", b.Health(id))
	}
	if len(transitions) != 2 || !transitions[0] || transitions[1] {
		t.Fatalf("transitions = %v", transitions)
	}
}

// Concurrent requests failing within the same second (one upstream restart)
// count once; the ratio still guards against sporadic failures.
func TestBreakerBurstAndRatio(t *testing.T) {
	b, now := testBreaker()
	id := uuid.New()
	tg := Target{Channel: id}
	for range 40 {
		b.Failure(tg, FailTransient, "boom", 0)
	}
	if b.State(id) != "closed" {
		t.Fatal("a burst within one second must not open")
	}
	*now = now.Add(time.Second)
	b.Failure(tg, FailTransient, "boom", 0)
	if b.State(id) != "closed" {
		t.Fatal("two failure seconds must not open")
	}

	b2, now2 := testBreaker()
	id2 := uuid.New()
	tg2 := Target{Channel: id2}
	for range 10 {
		b2.Success(tg2)
	}
	failSeconds(b2, now2, tg2, FailTransient, 5)
	if b2.State(id2) != "closed" {
		t.Fatal("5 failures out of 15 requests stay below 50%")
	}
	// Timeouts weigh half: six timeout seconds equal three failure seconds.
	b3, now3 := testBreaker()
	id3 := uuid.New()
	failSeconds(b3, now3, Target{Channel: id3}, FailTimeout, 5)
	if b3.State(id3) != "closed" {
		t.Fatal("5 timeout seconds (2.5) must not open")
	}
	*now3 = now3.Add(time.Second)
	failSeconds(b3, now3, Target{Channel: id3}, FailTimeout, 1)
	if b3.State(id3) != "open" {
		t.Fatal("6 timeout seconds must open")
	}
	// Failures older than the window are forgotten.
	b4, now4 := testBreaker()
	id4 := uuid.New()
	failSeconds(b4, now4, Target{Channel: id4}, FailTransient, 2)
	*now4 = now4.Add(61 * time.Second)
	failSeconds(b4, now4, Target{Channel: id4}, FailTransient, 1)
	if b4.State(id4) != "closed" {
		t.Fatal("old failures must expire")
	}
	*now4 = now4.Add(61 * time.Second)
	if b4.Health(id4).State != "healthy" {
		t.Fatal("no failure in the window is healthy")
	}
}

// Rate limits pause one model on the channel; model-specific failures open
// only that model; the channel and its other models keep serving.
func TestBreakerModelScope(t *testing.T) {
	b, now := testBreaker()
	id := uuid.New()
	luna, sol := Target{Channel: id, Model: "luna"}, Target{Channel: id, Model: "sol"}
	b.Failure(luna, FailRateLimit, "usage limit", 30*time.Second)
	if b.Allow(luna) || !b.Allow(sol) || b.State(id) != "closed" {
		t.Fatal("a 429 pauses only that model")
	}
	h := b.Health(id)
	if h.State != "degraded" || len(h.Models) != 1 || h.Models[0].State != "paused" || !h.Models[0].RetryAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("health = %+v", h)
	}
	*now = now.Add(31 * time.Second)
	if !b.Allow(luna) {
		t.Fatal("the pause ends after Retry-After")
	}
	failSeconds(b, now, luna, FailModel, 3)
	if b.Allow(luna) || !b.Allow(sol) || b.State(id) != "closed" {
		t.Fatal("model failures open only the model")
	}
	// A channel failure affects every model.
	b2, now2 := testBreaker()
	id2 := uuid.New()
	failSeconds(b2, now2, Target{Channel: id2, Model: "a"}, FailTransient, 3)
	if b2.Allow(Target{Channel: id2, Model: "b"}) {
		t.Fatal("channel failures block every model")
	}
}

// Last-resort attempts pass a blocked circuit, a few at a time; a success
// closes it, failures during the cooldown do not extend it.
func TestBreakerForceAllow(t *testing.T) {
	b, now := testBreaker()
	id := uuid.New()
	tg := Target{Channel: id, Model: "m"}
	failSeconds(b, now, tg, FailTransient, 3)
	until := b.RetryAt(tg)
	if !b.Blocked(tg) || !b.ForceAllow(tg) || !b.ForceAllow(tg) || !b.ForceAllow(tg) || b.ForceAllow(tg) {
		t.Fatal("three last-resort attempts at a time")
	}
	b.Failure(tg, FailTransient, "x", 0)
	if !b.RetryAt(tg).Equal(until) || !b.ForceAllow(tg) {
		t.Fatal("a failed last-resort attempt neither extends the cooldown nor keeps its slot")
	}
	b.Success(tg)
	if b.Blocked(tg) || b.State(id) != "closed" {
		t.Fatal("a successful last-resort attempt closes the circuit")
	}
}

// 401 opens with the long auth cooldown; the prober's success closes the
// circuit but keeps the backoff level.
func TestBreakerAuthAndProbeSuccess(t *testing.T) {
	b, now := testBreaker()
	id := uuid.New()
	tg := Target{Channel: id}
	failSeconds(b, now, tg, FailAuth, 3)
	if h := b.Health(id); h.Reason != ReasonAuth || h.RetryAt.Sub(*now) != time.Minute {
		t.Fatalf("auth health = %+v", h)
	}
	*now = now.Add(time.Minute)
	b.Allow(tg)
	b.Failure(tg, FailAuth, "401", 0)
	if b.RetryAt(tg).Sub(*now) != 2*time.Minute {
		t.Fatal("a failed auth probe doubles the auth cooldown")
	}
	b.ProbeSuccess(id)
	if b.State(id) != "closed" {
		t.Fatal("probe success closes")
	}
	*now = now.Add(time.Second)
	failSeconds(b, now, tg, FailTransient, 3)
	if b.RetryAt(tg).Sub(*now) != 10*time.Second {
		t.Fatalf("the backoff level survives a probe success: %s", b.RetryAt(tg).Sub(*now))
	}
}

func TestStatusFailKind(t *testing.T) {
	for status, want := range map[int]FailKind{401: FailAuth, 403: FailModel, 408: FailTimeout, 429: FailRateLimit, 502: FailTransient} {
		if got, ok := StatusFailKind(status); !ok || got != want {
			t.Errorf("StatusFailKind(%d) = %v %v", status, got, ok)
		}
	}
	if _, ok := StatusFailKind(404); ok {
		t.Error("404 says nothing about health")
	}
}
