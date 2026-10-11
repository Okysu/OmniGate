package channel

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func testBreaker() (*Breaker, *time.Time) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	b := NewBreaker(5, time.Minute, 30*time.Second)
	b.now = func() time.Time { return now }
	return b, &now
}

func TestBreakerNeedsRepeatedFailures(t *testing.T) {
	b, now := testBreaker()
	id := uuid.New()
	var transitions []bool
	b.OnTransition = func(_ uuid.UUID, unhealthy bool, _ string) { transitions = append(transitions, unhealthy) }
	for range 4 {
		b.Failure(id, "boom")
	}
	if b.State(id) != "closed" || b.Health(id).State != "degraded" {
		t.Fatalf("4 failures must not open: %s %+v", b.State(id), b.Health(id))
	}
	b.Failure(id, "boom")
	if b.State(id) != "open" {
		t.Fatalf("5 failures in a minute must open: %s", b.State(id))
	}
	if b.Allow(id) {
		t.Fatal("open circuit must reject during cooldown")
	}
	*now = now.Add(31 * time.Second)
	if b.State(id) != "half_open" || !b.Allow(id) || b.Allow(id) {
		t.Fatal("half-open lets exactly one probe through")
	}
	b.Failure(id, "still down")
	if b.State(id) != "open" {
		t.Fatal("a failed probe reopens the circuit")
	}
	*now = now.Add(31 * time.Second)
	b.Allow(id)
	b.Success(id)
	if b.State(id) != "closed" || b.Health(id).State != "healthy" {
		t.Fatalf("a successful probe closes the circuit: %+v", b.Health(id))
	}
	if len(transitions) != 2 || !transitions[0] || transitions[1] {
		t.Fatalf("transitions = %v", transitions)
	}
}

func TestBreakerWindowAndRatio(t *testing.T) {
	b, now := testBreaker()
	id := uuid.New()
	// Failures spread over more than the window never accumulate.
	for range 10 {
		b.Failure(id, "x")
		*now = now.Add(20 * time.Second)
		for range 3 {
			b.Success(id)
		}
	}
	if b.State(id) != "closed" {
		t.Fatal("sporadic failures must not open")
	}
	// Five failures among plenty of successes stay below the 50% ratio.
	b2, _ := testBreaker()
	id2 := uuid.New()
	for range 6 {
		b2.Success(id2)
	}
	for range 5 {
		b2.Failure(id2, "x")
	}
	if b2.State(id2) != "closed" {
		t.Fatal("5 failures out of 11 requests must not open")
	}
	b2.Failure(id2, "x")
	if b2.State(id2) != "open" {
		t.Fatal("6 failures out of 12 requests must open")
	}
	// Failures older than the window are forgotten.
	b3, now3 := testBreaker()
	id3 := uuid.New()
	for range 4 {
		b3.Failure(id3, "x")
	}
	*now3 = now3.Add(61 * time.Second)
	b3.Failure(id3, "x")
	if b3.State(id3) != "closed" || b3.Health(id3).State != "degraded" {
		t.Fatalf("old failures must expire: %s", b3.State(id3))
	}
	*now3 = now3.Add(61 * time.Second)
	if b3.Health(id3).State != "healthy" {
		t.Fatal("no failure in the window is healthy")
	}
}
