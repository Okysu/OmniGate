package pricing

import (
	"strings"
	"testing"
	"time"

	"omnigate/internal/money"
	"omnigate/internal/protocol"
)

func slot(days []int, start, end, mult string) ScheduleSlot {
	return ScheduleSlot{Days: days, Start: start, End: end, Multiplier: mult}
}

func mustSchedule(t *testing.T, tz string, slots ...ScheduleSlot) *Price {
	t.Helper()
	s, err := compileSchedule(slots)
	if err != nil {
		t.Fatal(err)
	}
	return &Price{InputPerM: money.MustParse("2"), OutputPerM: money.MustParse("8"), Schedule: s, ScheduleTimezone: tz}
}

func TestScheduleMatching(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	at := func(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, sh) }
	// DeepSeek off-peak: every day 00:30–08:30 at half price.
	p := mustSchedule(t, "Asia/Shanghai", slot(nil, "00:30", "08:30", "0.5"))
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{at(2026, 10, 9, 0, 29), "1"}, {at(2026, 10, 9, 0, 30), "0.5"}, {at(2026, 10, 9, 8, 29), "0.5"},
		{at(2026, 10, 9, 8, 30), "1"}, {at(2026, 10, 9, 23, 59), "1"},
		// The schedule is evaluated in its own timezone: 17:00 UTC = 01:00 Shanghai.
		{time.Date(2026, 10, 9, 17, 0, 0, 0, time.UTC), "0.5"},
	} {
		if got := p.ScheduleMultiplier(c.at).String(); got != c.want {
			t.Errorf("%s: multiplier %s, want %s", c.at, got, c.want)
		}
	}

	// Cross-midnight with a weekday filter: Friday (5) and Saturday (6)
	// 22:00–02:00; the part after midnight belongs to the next day.
	p = mustSchedule(t, "UTC", slot([]int{5, 6}, "22:00", "02:00", "0.25"), slot([]int{1}, "09:00", "24:00", "2"))
	utc := func(d, h, mi int) time.Time { return time.Date(2026, 10, d, h, mi, 0, 0, time.UTC) } // Oct 9 2026 is a Friday
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{utc(9, 21, 59), "1"}, {utc(9, 22, 0), "0.25"}, {utc(10, 1, 59), "0.25"}, // Fri night into Sat
		{utc(10, 2, 0), "1"}, {utc(10, 23, 0), "0.25"}, {utc(11, 1, 0), "0.25"}, // Sat night into Sun
		{utc(11, 22, 30), "1"}, {utc(12, 1, 0), "1"}, // Sunday night is not covered
		{utc(12, 9, 0), "2"}, {utc(12, 23, 59), "2"}, {utc(13, 0, 0), "1"}, // Monday until 24:00
		{utc(8, 23, 0), "1"}, // Thursday
	} {
		if got := p.ScheduleMultiplier(c.at).String(); got != c.want {
			t.Errorf("%s (%s): multiplier %s, want %s", c.at, c.at.Weekday(), got, c.want)
		}
	}

	// Compute applies schedule × group with one rounding step; cost prices
	// pass One.
	p = mustSchedule(t, "Asia/Shanghai", slot(nil, "00:30", "08:30", "0.5"))
	u := protocol.Usage{Input: 1_000_000, Output: 1_000_000} // base 10
	night, day := at(2026, 10, 9, 3, 0), at(2026, 10, 9, 12, 0)
	for _, c := range []struct {
		at    time.Time
		group Multiplier
		want  string
	}{
		{night, One, "5"}, {day, One, "10"}, {night, 800_000_000, "4"}, {day, 800_000_000, "8"}, {night, 0, "0"},
		{day, 3 * One, "30"},
	} {
		if got, err := p.Compute(u, c.at, c.group); err != nil || got.String() != c.want {
			t.Errorf("compute at %s × %s = %s %v, want %s", c.at, c.group, got, err, c.want)
		}
	}
	if got := Product(500_000_000, 800_000_000); got != "0.4" {
		t.Fatalf("product = %s", got)
	}
	if got := Product(One, One); got != "1" {
		t.Fatalf("product = %s", got)
	}
	if got := Product(123_456_789, 3*One); got != "0.370370367" {
		t.Fatalf("product = %s", got)
	}
	// One rounding step: 0.000000003 × 0.5 × 0.5 = 0.00000000075 → 0.000000001.
	if got, _ := applyFactors(3, 500_000_000, 500_000_000); got != 1 {
		t.Fatalf("rounded = %d", got)
	}
}

func TestScheduleValidation(t *testing.T) {
	for _, c := range []struct {
		slots []ScheduleSlot
		err   string
	}{
		{[]ScheduleSlot{slot(nil, "00:30", "08:30", "0.5"), slot(nil, "08:00", "09:00", "2")}, "重叠"},
		{[]ScheduleSlot{slot([]int{6}, "22:00", "02:00", "0.5"), slot([]int{0}, "01:00", "03:00", "2")}, "重叠"},
		{[]ScheduleSlot{slot(nil, "22:00", "02:00", "0.5"), slot([]int{3}, "00:00", "01:00", "2")}, "重叠"},
		{[]ScheduleSlot{slot(nil, "8:00", "09:00", "1")}, "HH:MM"},
		{[]ScheduleSlot{slot(nil, "24:00", "09:00", "1")}, "HH:MM"},
		{[]ScheduleSlot{slot(nil, "09:00", "09:00", "1")}, "不能相同"},
		{[]ScheduleSlot{slot(nil, "22:00", "24:00", "11")}, "multiplier"},
		{[]ScheduleSlot{slot(nil, "22:00", "24:00", "-1")}, "multiplier"},
		{[]ScheduleSlot{slot([]int{7}, "22:00", "23:00", "1")}, "days"},
	} {
		if _, err := compileSchedule(c.slots); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%v: err = %v, want %q", c.slots, err, c.err)
		}
	}
	// Adjacent and disjoint slots are fine; weekday filters avoid overlaps.
	ok := [][]ScheduleSlot{
		{slot(nil, "00:00", "08:00", "0.5"), slot(nil, "08:00", "24:00", "1.2")},
		{slot([]int{6}, "22:00", "02:00", "0.5"), slot([]int{0}, "02:00", "03:00", "2"), slot([]int{1, 2}, "01:00", "03:00", "2")},
		{slot([]int{1, 1, 2}, "10:00", "11:00", "0")},
	}
	for _, slots := range ok {
		if _, err := compileSchedule(slots); err != nil {
			t.Errorf("%v: %v", slots, err)
		}
	}
	if s, _ := compileSchedule(ok[2]); len(s[0].Days) != 2 || s[0].Multiplier != "0" {
		t.Fatalf("normalized = %+v", s)
	}
	if s, err := compileSchedule([]ScheduleSlot{}); s != nil || err != nil {
		t.Fatal("empty schedule must compile to none")
	}
}
