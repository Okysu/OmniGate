package routing

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/identity"
	"omnigate/internal/money"
)

func TestMatchModel(t *testing.T) {
	cases := []struct {
		pattern, model string
		want           bool
	}{
		{"gpt-4o", "gpt-4o", true},
		{"gpt-4o", "gpt-4o-mini", false},
		{"gpt-*", "gpt-4o", true},
		{"gpt-*", "gpt-", true},
		{"gpt-*", "claude-3", false},
		{"*", "anything/at-all", true},
		{"*", "", true},
		{"*-mini", "gpt-4o-mini", true},
		{"*-mini", "gpt-4o-mini-2", false},
		{"claude-*-sonnet-*", "claude-3-5-sonnet-latest", true},
		{"claude-*-sonnet-*", "claude-3-5-haiku-latest", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "acb", false},
		{"ab*ba", "aba", false}, // prefix and suffix must not overlap
		{"openai/*", "openai/gpt-4o", true},
		{"gpt-?", "gpt-4", false}, // '?' is literal
		{"gpt-[4]", "gpt-[4]", true},
	}
	for _, c := range cases {
		if got := MatchModel(c.pattern, c.model); got != c.want {
			t.Errorf("MatchModel(%q, %q) = %v, want %v", c.pattern, c.model, got, c.want)
		}
	}
}

func TestRuleMatches(t *testing.T) {
	r := &Rule{Enabled: true, Match: Match{Models: []string{"gpt-*", "o3"}, Roles: []identity.Role{identity.RoleUser}}}
	if !r.Matches("gpt-4o", identity.RoleUser) || !r.Matches("o3", identity.RoleUser) {
		t.Fatal("expected match")
	}
	if r.Matches("gpt-4o", identity.RoleSystemAdmin) {
		t.Fatal("role filter ignored")
	}
	if r.Matches("o3-mini", identity.RoleUser) {
		t.Fatal("exact name matched a longer model")
	}
	r.Match.Roles = nil
	if !r.Matches("gpt-4o", identity.RoleAuditor) {
		t.Fatal("empty roles should match every role")
	}
	r.Enabled = false
	if r.Matches("gpt-4o", identity.RoleUser) {
		t.Fatal("disabled rule matched")
	}
}

func TestInputDefaultsAndValidation(t *testing.T) {
	name := "r"
	in := Input{Name: &name, Match: &Match{Models: []string{" gpt-* ", "gpt-*"}}}
	r := &Rule{Enabled: true}
	if d := in.apply(r, true); len(d) > 0 {
		t.Fatalf("details = %v", d)
	}
	if r.Strategy != StrategyPriority || r.ProtocolPreference != PreferNative || r.Retry.MaxAttempts != 3 ||
		!slices.Equal(r.Retry.RetryOn, DefaultRetryClasses) || len(r.Match.Models) != 1 || r.Targets == nil || r.FallbackModels == nil {
		t.Fatalf("defaults = %+v", r)
	}

	bad := "nope"
	zero := 0
	w := 5000
	in = Input{Name: new(string), Match: &Match{Models: []string{}, Roles: []identity.Role{"root"}}, Strategy: &bad, ProtocolPreference: &bad,
		Retry:          &RetryInput{MaxAttempts: &zero, RetryOn: []string{"server_error", "teapot"}},
		FallbackModels: &[]string{"a", "b", "c", "d", "e", "f"},
		Targets:        &[]Target{{ChannelID: uuid.Nil, Weight: &w}, {ChannelID: uuid.Nil}}}
	d := in.apply(&Rule{}, true)
	for _, k := range []string{"name", "match.models", "match.roles", "strategy", "protocolPreference", "retry.maxAttempts",
		"retry.retryOn", "fallbackModels", "targets[0].weight", "targets[1].channelId"} {
		if d[k] == nil {
			t.Errorf("missing detail %s (have %v)", k, d)
		}
	}
	if d := (&Input{FallbackModels: &[]string{"gpt-*"}}).apply(&Rule{}, false); d["fallbackModels"] == nil || d["version"] == nil {
		t.Fatalf("update details = %v", d)
	}
}

func TestRetryAllows(t *testing.T) {
	r := Retry{MaxAttempts: 2, RetryOn: []string{RetryRateLimit, RetryTimeout}}
	if !r.Allows(RetryRateLimit) || !r.Allows(RetryTimeout) {
		t.Fatal("listed classes must be retryable")
	}
	if r.Allows(RetryServerError) || r.Allows(RetryNetwork) || r.Allows("") {
		t.Fatal("unlisted or empty classes must not be retried")
	}
}

func names(order []int, cands []string) string {
	s := ""
	for i, j := range order {
		if i > 0 {
			s += ","
		}
		s += cands[j]
	}
	return s
}

func fp(v float64) *float64 { return &v }

func amt(s string) *money.Amount { a := money.MustParse(s); return &a }

func TestOrderStrategies(t *testing.T) {
	rnd := rand.Float64
	labels := []string{"a", "b", "c", "d"}

	// priority: priority desc, then native protocol first.
	cands := []Candidate{{Priority: 0, Weight: 1, Hops: 0}, {Priority: 5, Weight: 1, Hops: 1}, {Priority: 5, Weight: 1, Hops: 0}, {Priority: 1, Weight: 1, Hops: 2}}
	for range 100 {
		if got := names(Order(StrategyPriority, PreferNative, cands, 0, rnd), labels); got != "c,b,d,a" {
			t.Fatalf("priority = %s", got)
		}
	}
	// ignore: protocol fit no longer separates equal priorities.
	seenB := false
	for range 200 {
		if Order(StrategyPriority, PreferIgnore, cands, 0, rnd)[0] == 1 {
			seenB = true
		}
	}
	if !seenB {
		t.Fatal("protocolPreference=ignore still prefers native channels")
	}

	// weighted: priority ignored; weights spread traffic.
	w := []Candidate{{Priority: 100, Weight: 1}, {Priority: 0, Weight: 3}}
	firstB := 0
	for range 4000 {
		if Order(StrategyWeighted, PreferNative, w, 0, rnd)[0] == 1 {
			firstB++
		}
	}
	if firstB < 2700 || firstB > 3300 {
		t.Fatalf("weighted 1:3 → b first %d/4000", firstB)
	}

	// round_robin: rotates the input order; native first.
	rr := []Candidate{{Weight: 1}, {Weight: 1}, {Weight: 1}}
	var seq []string
	for i := range uint64(4) {
		seq = append(seq, names(Order(StrategyRoundRobin, PreferNative, rr, i, rnd), labels))
	}
	if fmt.Sprint(seq) != "[a,b,c b,c,a c,a,b a,b,c]" {
		t.Fatalf("round robin = %v", seq)
	}
	rr[2].Hops = 1
	if got := names(Order(StrategyRoundRobin, PreferNative, rr, 2, rnd), labels); got != "a,b,c" {
		t.Fatalf("round robin native first = %s", got)
	}

	// least_latency: ascending, no data last.
	ll := []Candidate{{LatencyMs: nil, Weight: 1}, {LatencyMs: fp(300), Weight: 1}, {LatencyMs: fp(120), Weight: 1}, {LatencyMs: fp(900), Priority: 99, Weight: 1}}
	if got := names(Order(StrategyLeastLatency, PreferNative, ll, 0, rnd), labels); got != "c,b,d,a" {
		t.Fatalf("least latency = %s", got)
	}

	// lowest_cost: ascending, no price last; equal cost prefers native.
	lc := []Candidate{{Cost: amt("3"), Hops: 1, Weight: 1}, {Cost: nil, Weight: 1}, {Cost: amt("3"), Hops: 0, Weight: 1}, {Cost: amt("0.5"), Hops: 2, Weight: 1}}
	for range 50 {
		if got := names(Order(StrategyLowestCost, PreferNative, lc, 0, rnd), labels); got != "d,c,a,b" {
			t.Fatalf("lowest cost = %s", got)
		}
	}
	if got := Order(StrategyPriority, PreferNative, nil, 3, rnd); len(got) != 0 {
		t.Fatalf("empty = %v", got)
	}
}

func TestLatencyEWMA(t *testing.T) {
	l := NewLatency()
	ch := uuid.New()
	if _, ok := l.Get(ch, "m"); ok {
		t.Fatal("no data expected")
	}
	l.Observe(ch, "m", 100*time.Millisecond)
	l.Observe(ch, "m", 200*time.Millisecond)
	v, _ := l.Get(ch, "m")
	if v < 129.9 || v > 130.1 {
		t.Fatalf("ewma = %v, want 130", v)
	}
	if _, ok := l.Get(ch, "other"); ok {
		t.Fatal("latency is per model")
	}
}
