package routing

import (
	"math"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
)

// Candidate is one channel as seen by the ordering strategies.
type Candidate struct {
	Priority int
	Weight   int
	// Hops is the number of protocol conversion steps (0 = native).
	Hops int
	// LatencyMs is the recent time-to-first-byte EWMA (nil = no data).
	LatencyMs *float64
	// Cost is input + output cost price per million tokens (nil = no price).
	Cost *money.Amount
}

// Order returns the attempt order of cands as indices into cands.
//
// Every strategy first groups candidates into tiers (priority: equal priority;
// weighted and round_robin: one tier; least_latency / lowest_cost: equal
// latency / cost, with "no data" last). With native_first, candidates needing
// fewer protocol conversions come first within a tier. The remaining ties are
// broken by a weighted shuffle (Efraimidis–Spirakis: key = u^(1/w), highest
// first) or, for round_robin, by rotating the input order by rr.
//
// rnd returns uniform values in [0, 1).
func Order(strategy, preference string, cands []Candidate, rr uint64, rnd func() float64) []int {
	type item struct {
		i   int
		key float64
		rot int
	}
	n := len(cands)
	items := make([]item, n)
	for i, c := range cands {
		items[i] = item{i: i, key: math.Pow(rnd(), 1/float64(max(c.Weight, 1)))}
		if n > 0 {
			items[i].rot = int((uint64(i) + uint64(n) - rr%uint64(n)) % uint64(n))
		}
	}
	native := preference != PreferIgnore
	slices.SortStableFunc(items, func(x, y item) int {
		a, b := cands[x.i], cands[y.i]
		if c := tier(strategy, a, b); c != 0 {
			return c
		}
		if native && a.Hops != b.Hops {
			return a.Hops - b.Hops
		}
		if strategy == StrategyRoundRobin {
			return x.rot - y.rot
		}
		switch {
		case x.key > y.key:
			return -1
		case x.key < y.key:
			return 1
		}
		return 0
	})
	out := make([]int, n)
	for i, it := range items {
		out[i] = it.i
	}
	return out
}

func tier(strategy string, a, b Candidate) int {
	switch strategy {
	case StrategyWeighted, StrategyRoundRobin:
		return 0
	case StrategyLeastLatency:
		return compareNilLast(a.LatencyMs, b.LatencyMs, func(x, y float64) int {
			switch {
			case x < y:
				return -1
			case x > y:
				return 1
			}
			return 0
		})
	case StrategyLowestCost:
		return compareNilLast(a.Cost, b.Cost, func(x, y money.Amount) int {
			switch {
			case x < y:
				return -1
			case x > y:
				return 1
			}
			return 0
		})
	default: // priority
		return b.Priority - a.Priority
	}
}

func compareNilLast[T any](a, b *T, cmp func(T, T) int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return cmp(*a, *b)
}

// Latency tracks an exponentially weighted moving average of upstream
// time-to-first-byte per channel and logical model (in-process only).
type Latency struct {
	mu sync.Mutex
	m  map[latencyKey]float64
}

type latencyKey struct {
	channel uuid.UUID
	model   string
}

// latencyAlpha weights the newest observation.
const latencyAlpha = 0.3

func NewLatency() *Latency { return &Latency{m: map[latencyKey]float64{}} }

// Observe records a successful attempt's time to first byte.
func (l *Latency) Observe(channel uuid.UUID, model string, d time.Duration) {
	ms := float64(d) / float64(time.Millisecond)
	k := latencyKey{channel, model}
	l.mu.Lock()
	defer l.mu.Unlock()
	if old, ok := l.m[k]; ok {
		ms = old + latencyAlpha*(ms-old)
	}
	l.m[k] = ms
}

// Get returns the current average in milliseconds.
func (l *Latency) Get(channel uuid.UUID, model string) (float64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.m[latencyKey{channel, model}]
	return v, ok
}
