// Package limits implements the per-user and per-key limits of
// docs/contracts/phase8-api.md §2: the user group's requests per minute
// (in-process token bucket) and per day, daily / monthly spend and the API
// key spend limit, backed by usage_counters rows that settlement increments
// in the wallet transaction.
package limits

import "time"

// Window kinds of usage counters.
type Window string

const (
	Day   Window = "day"
	Week  Window = "week"
	Month Window = "month"
	Total Window = "total"
)

// Scopes of usage counters.
const (
	ScopeUser = "user"
	ScopeKey  = "key"
)

// totalStart is the window start of the 'total' window.
var totalStart = time.Unix(0, 0).UTC()

// Start returns the start of the window containing at, in loc (weeks start
// on Monday), as a UTC instant.
func Start(w Window, at time.Time, loc *time.Location) time.Time {
	t := at.In(loc)
	switch w {
	case Day:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).UTC()
	case Week:
		back := (int(t.Weekday()) + 6) % 7 // days since Monday
		return time.Date(t.Year(), t.Month(), t.Day()-back, 0, 0, 0, 0, loc).UTC()
	case Month:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc).UTC()
	}
	return totalStart
}

// End returns when the window containing at ends (nil for 'total').
func End(w Window, at time.Time, loc *time.Location) *time.Time {
	s := Start(w, at, loc).In(loc)
	var e time.Time
	switch w {
	case Day:
		e = time.Date(s.Year(), s.Month(), s.Day()+1, 0, 0, 0, 0, loc)
	case Week:
		e = time.Date(s.Year(), s.Month(), s.Day()+7, 0, 0, 0, 0, loc)
	case Month:
		e = time.Date(s.Year(), s.Month()+1, 1, 0, 0, 0, 0, loc)
	default:
		return nil
	}
	e = e.UTC()
	return &e
}

// Valid reports whether w is a known window.
func (w Window) Valid() bool { return w == Day || w == Week || w == Month || w == Total }
