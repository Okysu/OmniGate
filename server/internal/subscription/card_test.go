package subscription

import (
	"testing"
	"time"
)

// TestCardMatches: cards match session / rolling rules by window length only
// (phase11-api.md §2).
func TestCardMatches(t *testing.T) {
	rule := func(kind, dur string) *compiledRule {
		t.Helper()
		r := Rule{ID: "x", Meter: MeterRequests, Limit: "1", Window: Window{Kind: kind, Duration: dur}}
		if kind == WindowPeriod {
			r.Window = Window{Kind: kind, Every: dur}
		}
		r.normalize()
		c, err := compileRule(r, nil, "")
		if err != nil {
			t.Fatalf("%s %s: %v", kind, dur, err)
		}
		return c
	}
	cases := []struct {
		r                  *compiledRule
		five, weekly, both bool
	}{
		{rule(WindowSession, "5h"), true, false, true},
		{rule(WindowRolling, "300m"), true, false, true},
		{rule(WindowSession, "7d"), false, true, true},
		{rule(WindowRolling, "168h"), false, true, true},
		{rule(WindowSession, "6h"), false, false, false},
		{rule(WindowRolling, "1d"), false, false, false},
		{rule(WindowPeriod, "7d"), false, false, false},
		{rule(WindowLifetime, ""), false, false, false},
	}
	for i, c := range cases {
		if cardMatches(CardKind5h, c.r) != c.five || cardMatches(CardKindWeekly, c.r) != c.weekly ||
			cardMatches(CardKindBoth, c.r) != c.both || cardMatches("daily", c.r) {
			t.Errorf("case %d (%s %s): mismatch", i, c.r.Window.Kind, c.r.dur)
		}
	}
}

func TestCardStatus(t *testing.T) {
	now := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		stored  string
		expires *time.Time
		want    string
	}{
		{CardAvailable, nil, CardAvailable},
		{CardAvailable, ptr(now.Add(time.Second)), CardAvailable},
		{CardAvailable, ptr(now), CardExpired},
		{CardUsed, ptr(now.Add(-time.Hour)), CardUsed},
		{CardRevoked, ptr(now.Add(-time.Hour)), CardRevoked},
	} {
		if got := cardStatus(c.stored, c.expires, now); got != c.want {
			t.Errorf("cardStatus(%s, %v) = %s, want %s", c.stored, c.expires, got, c.want)
		}
	}
	if !ValidCardKind(CardKindBoth) || ValidCardKind("") || CardKindLabel(CardKindWeekly) != "周重置卡" {
		t.Fatal("kinds")
	}
}
