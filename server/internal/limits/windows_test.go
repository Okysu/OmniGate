package limits

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/keys"
	"omnigate/internal/money"
	"omnigate/internal/pricing"
	"omnigate/internal/usergroup"
)

func TestWindows(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	ny, _ := time.LoadLocation("America/New_York")
	at := time.Date(2026, 10, 9, 1, 30, 0, 0, sh) // Friday 01:30 in Shanghai = Thursday 17:30 UTC
	for _, c := range []struct {
		w          Window
		loc        *time.Location
		start, end string
	}{
		{Day, sh, "2026-10-08T16:00:00Z", "2026-10-09T16:00:00Z"},
		{Week, sh, "2026-10-04T16:00:00Z", "2026-10-11T16:00:00Z"}, // Monday Oct 5 00:00 +08
		{Month, sh, "2026-09-30T16:00:00Z", "2026-10-31T16:00:00Z"},
		{Day, time.UTC, "2026-10-08T00:00:00Z", "2026-10-09T00:00:00Z"},
		{Week, time.UTC, "2026-10-05T00:00:00Z", "2026-10-12T00:00:00Z"},
		{Total, sh, "1970-01-01T00:00:00Z", ""},
	} {
		if got := Start(c.w, at, c.loc).Format(time.RFC3339); got != c.start {
			t.Errorf("%s %s start = %s, want %s", c.w, c.loc, got, c.start)
		}
		end := End(c.w, at, c.loc)
		if (end == nil) != (c.end == "") || (end != nil && end.Format(time.RFC3339) != c.end) {
			t.Errorf("%s %s end = %v, want %s", c.w, c.loc, end, c.end)
		}
	}
	// A Monday belongs to its own week; a Sunday to the week before.
	if got := Start(Week, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), time.UTC); got.Day() != 5 {
		t.Errorf("monday week start = %v", got)
	}
	if got := Start(Week, time.Date(2026, 10, 11, 23, 0, 0, 0, time.UTC), time.UTC); got.Day() != 5 {
		t.Errorf("sunday week start = %v", got)
	}
	// DST: the New York day of the spring-forward change is 23 hours long.
	dst := time.Date(2026, 3, 8, 12, 0, 0, 0, ny)
	if d := End(Day, dst, ny).Sub(Start(Day, dst, ny)); d != 23*time.Hour {
		t.Errorf("DST day = %v", d)
	}
	// Month end in December rolls over the year.
	if got := End(Month, time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC), time.UTC).Format(time.RFC3339); got != "2027-01-01T00:00:00Z" {
		t.Errorf("december end = %s", got)
	}
}

func TestIncrements(t *testing.T) {
	g := &usergroup.Group{Multiplier: pricing.One, Timezone: "UTC"}
	user, key := uuid.New(), &keys.Key{ID: uuid.New(), Name: "k"}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if incs := Increments(Usage{UserID: user, Key: key, Group: g, At: at}); incs != nil {
		t.Fatalf("nothing to count: %v", incs)
	}
	// Counted, free (own channel): the user's day and month requests only.
	incs := Increments(Usage{UserID: user, Key: key, Group: g, At: at, Counted: true})
	if len(incs) != 2 || incs[0].Requests != 1 || incs[0].Charge != 0 || incs[1].Window != Month {
		t.Fatalf("free increments = %+v", incs)
	}
	// Charged: plus the key's four windows (charge only).
	incs = Increments(Usage{UserID: user, Key: key, Group: g, At: at, Counted: true, Charge: money.MustParse("0.5")})
	if len(incs) != 6 {
		t.Fatalf("charged increments = %+v", incs)
	}
	for _, in := range incs[2:] {
		if in.Scope != ScopeKey || in.Requests != 0 || in.Charge != money.MustParse("0.5") {
			t.Fatalf("key increment = %+v", in)
		}
	}
	for _, c := range []struct {
		spent, limit string
		near         bool
	}{{"0.79", "1", false}, {"0.8", "1", true}, {"1.2", "1", true}, {"0.000000004", "0.000000005", true}, {"0.000000003", "0.000000005", false}} {
		if got := nearLimit(money.MustParse(c.spent), money.MustParse(c.limit)); got != c.near {
			t.Errorf("nearLimit(%s, %s) = %v", c.spent, c.limit, got)
		}
	}
}
