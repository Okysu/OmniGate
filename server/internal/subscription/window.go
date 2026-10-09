package subscription

import (
	"slices"
	"time"
)

// usageRow is one quota_usage row of a (subscription, rule).
type usageRow struct {
	start time.Time
	used  Dec
}

// ruleState is the current window of one rule of one subscription.
type ruleState struct {
	used        Dec
	windowStart *time.Time // nil: session window not started
	resetsAt    *time.Time // nil: never resets (lifetime) or nothing to reset
	exceeded    bool       // used >= limit
	// retryAt is when the rule admits requests again (only set when exceeded);
	// nil means never (lifetime).
	retryAt *time.Time
}

func ptr[T any](v T) *T { return &v }

// calendarWindow returns the calendar day/week/month containing now in loc.
// Weeks start on Monday.
func calendarWindow(unit string, loc *time.Location, now time.Time) (start, end time.Time) {
	t := now.In(loc)
	y, m, d := t.Date()
	switch unit {
	case "week":
		off := (int(t.Weekday()) + 6) % 7 // Monday = 0
		start = time.Date(y, m, d-off, 0, 0, 0, 0, loc)
		end = time.Date(y, m, d-off+7, 0, 0, 0, 0, loc)
	case "month":
		start = time.Date(y, m, 1, 0, 0, 0, 0, loc)
		end = time.Date(y, m+1, 1, 0, 0, 0, 0, loc)
	default: // day
		start = time.Date(y, m, d, 0, 0, 0, 0, loc)
		end = time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	}
	return start.UTC(), end.UTC()
}

// periodWindow returns the k-th period of length every, aligned to startsAt,
// that contains now (the first period when now precedes startsAt).
func periodWindow(startsAt time.Time, every time.Duration, now time.Time) (start, end time.Time) {
	k := time.Duration(0)
	if now.After(startsAt) {
		k = now.Sub(startsAt) / every
	}
	start = startsAt.Add(k * every)
	return start.UTC(), start.Add(every).UTC()
}

// bucketStart is the rolling-window bucket of t.
func bucketStart(t time.Time) time.Time { return t.UTC().Truncate(rollingBucket) }

// usageSince is the lowest window_start that can matter for the rule's state at
// now; rows before it are never read.
func (c *compiledRule) usageSince(startsAt, now time.Time) time.Time {
	switch c.Window.Kind {
	case WindowCalendar:
		s, _ := calendarWindow(c.Window.Unit, c.loc, now)
		return s
	case WindowPeriod:
		s, _ := periodWindow(startsAt, c.dur, now)
		return s
	case WindowRolling, WindowSession:
		return now.Add(-c.dur)
	default: // lifetime
		return startsAt.UTC()
	}
}

// state computes the rule's current window at now from rows, the usage rows of
// this (subscription, rule) with window_start ≥ usageSince (any order).
func (c *compiledRule) state(startsAt, now time.Time, rows []usageRow) ruleState {
	var st ruleState
	switch c.Window.Kind {
	case WindowCalendar, WindowPeriod, WindowLifetime:
		var start time.Time
		var end *time.Time
		switch c.Window.Kind {
		case WindowCalendar:
			s, e := calendarWindow(c.Window.Unit, c.loc, now)
			start, end = s, &e
		case WindowPeriod:
			s, e := periodWindow(startsAt, c.dur, now)
			start, end = s, &e
		default:
			start = startsAt.UTC()
		}
		for _, r := range rows {
			if r.start.Equal(start) {
				st.used = st.used.Add(r.used)
			}
		}
		st.windowStart, st.resetsAt, st.retryAt = ptr(start), end, end
	case WindowRolling:
		since := now.Add(-c.dur)
		live := make([]usageRow, 0, len(rows))
		for _, r := range rows {
			if r.start.After(since) {
				live = append(live, r)
				st.used = st.used.Add(r.used)
			}
		}
		st.windowStart = ptr(since.UTC())
		if len(live) > 0 {
			slices.SortFunc(live, func(a, b usageRow) int { return a.start.Compare(b.start) })
			st.resetsAt = ptr(live[0].start.Add(c.dur).UTC())
			// Admitted again once enough of the oldest buckets have expired to
			// bring the total below the limit.
			rest := st.used
			for _, r := range live {
				rest = rest.Sub(r.used)
				if rest.Cmp(c.limit) < 0 {
					st.retryAt = ptr(r.start.Add(c.dur).UTC())
					break
				}
			}
		}
	case WindowSession:
		if r, ok := c.activeSession(now, rows); ok {
			st.used = r.used
			st.windowStart = ptr(r.start.UTC())
			st.resetsAt = ptr(r.start.Add(c.dur).UTC())
			st.retryAt = st.resetsAt
		}
	}
	st.exceeded = st.used.Cmp(c.limit) >= 0
	if !st.exceeded {
		st.retryAt = nil
	}
	return st
}

// activeSession returns the newest session row started after at-duration.
func (c *compiledRule) activeSession(at time.Time, rows []usageRow) (usageRow, bool) {
	since := at.Add(-c.dur)
	var best usageRow
	found := false
	for _, r := range rows {
		if r.start.After(since) && (!found || r.start.After(best.start)) {
			best, found = r, true
		}
	}
	return best, found
}

// recordWindow is the window_start that usage at `at` is added to. rows are the
// rule's rows with window_start ≥ usageSince(at) (only consulted for sessions).
func (c *compiledRule) recordWindow(startsAt, at time.Time, rows []usageRow) time.Time {
	switch c.Window.Kind {
	case WindowCalendar:
		s, _ := calendarWindow(c.Window.Unit, c.loc, at)
		return s
	case WindowPeriod:
		s, _ := periodWindow(startsAt, c.dur, at)
		return s
	case WindowRolling:
		return bucketStart(at)
	case WindowSession:
		if r, ok := c.activeSession(at, rows); ok {
			return r.start.UTC()
		}
		// No live session: this request opens one.
		return at.UTC().Truncate(time.Second)
	default:
		return startsAt.UTC()
	}
}
