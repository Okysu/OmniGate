package subscription

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
	"omnigate/internal/protocol"
)

func TestParseDuration(t *testing.T) {
	ok := map[string]time.Duration{
		"5m": 5 * time.Minute, "1h": time.Hour, "5h": 5 * time.Hour, "30d": 30 * day, "366d": 366 * day,
		"90m": 90 * time.Minute,
	}
	for in, want := range ok {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "m", "5", "05m", "0m", "-5m", "5s", "5w", "1.5h", "5 h", "5H", "+5m", "1h30m",
		"9999999d", "99999999m"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) succeeded", in)
		}
	}
	// Ranges.
	for in, valid := range map[string]bool{"4m": false, "5m": true, "31d": true, "32d": false, "744h": true, "745h": false} {
		if _, err := ParseWindowDuration(in); (err == nil) != valid {
			t.Errorf("ParseWindowDuration(%q) err=%v", in, err)
		}
	}
	for in, valid := range map[string]bool{"59m": false, "60m": true, "1h": true, "366d": true, "367d": false} {
		if _, err := ParsePeriod(in); (err == nil) != valid {
			t.Errorf("ParsePeriod(%q) err=%v", in, err)
		}
	}
}

func TestDec(t *testing.T) {
	for in, want := range map[string]string{"0": "0", "12": "12", "0.5": "0.5", "1.230000000": "1.23",
		"-3.25": "-3.25", "00012.10": "12.1", "0.000000001": "0.000000001"} {
		d, err := ParseDec(in)
		if err != nil || d.String() != want {
			t.Errorf("ParseDec(%q) = %s, %v; want %s", in, d, err, want)
		}
	}
	for _, in := range []string{"", ".5", "5.", "1e3", "abc", "1.0000000001", "--1", "1,5",
		"1" + strings.Repeat("0", 30)} {
		if _, err := ParseDec(in); err == nil {
			t.Errorf("ParseDec(%q) succeeded", in)
		}
	}
	if !MustDec("3").IsInt() || MustDec("3.5").IsInt() {
		t.Error("IsInt")
	}
	cases := []struct{ a, b, want string }{
		{"1000", "1.5", "1500"},
		{"0.000000001", "0.5", "0.000000001"}, // half away from zero
		{"0.000000001", "0.4", "0"},
		{"123456789012345678", "1000", "123456789012345678000"}, // beyond int64 nano
		{"7", "0", "0"},
	}
	for _, c := range cases {
		if got := MustDec(c.a).Mul(MustDec(c.b)).String(); got != c.want {
			t.Errorf("%s × %s = %s, want %s", c.a, c.b, got, c.want)
		}
	}
	if (Dec{}).String() != "0" || DecNano(1_500_000_000).String() != "1.5" || DecInt(-2).String() != "-2" {
		t.Error("constructors")
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func rule(t *testing.T, r Rule) *compiledRule {
	t.Helper()
	r.normalize()
	details := map[string]any{}
	c, err := compileRule(r, details, "")
	if err != nil {
		t.Fatalf("compile %+v: %v", details, err)
	}
	return c
}

func TestCalendarWindows(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	ny, _ := time.LoadLocation("America/New_York")
	cases := []struct {
		unit       string
		loc        *time.Location
		now        string
		start, end string
	}{
		{"day", time.UTC, "2026-10-08T13:00:00Z", "2026-10-08T00:00:00Z", "2026-10-09T00:00:00Z"},
		// 2026-10-07 23:30 UTC is Thursday 07:30 in Shanghai.
		{"day", sh, "2026-10-07T23:30:00Z", "2026-10-07T16:00:00Z", "2026-10-08T16:00:00Z"},
		// Shanghai week starts Monday 00:00 +08 = Sunday 16:00 UTC.
		{"week", sh, "2026-10-08T05:00:00Z", "2026-10-04T16:00:00Z", "2026-10-11T16:00:00Z"},
		// Sunday 15:59 UTC is still the previous Shanghai week (Sunday 23:59 local)...
		{"week", sh, "2026-10-11T15:59:59Z", "2026-10-04T16:00:00Z", "2026-10-11T16:00:00Z"},
		// ...and 16:00 UTC opens the next one (Monday 00:00 local).
		{"week", sh, "2026-10-11T16:00:00Z", "2026-10-11T16:00:00Z", "2026-10-18T16:00:00Z"},
		// UTC week on a Monday and on a Sunday.
		{"week", time.UTC, "2026-10-05T00:00:00Z", "2026-10-05T00:00:00Z", "2026-10-12T00:00:00Z"},
		{"week", time.UTC, "2026-10-11T23:59:59Z", "2026-10-05T00:00:00Z", "2026-10-12T00:00:00Z"},
		// Month end in Shanghai: Oct 31 20:00 UTC is already Nov 1 04:00 local.
		{"month", sh, "2026-10-31T20:00:00Z", "2026-10-31T16:00:00Z", "2026-11-30T16:00:00Z"},
		{"month", sh, "2026-10-31T15:59:59Z", "2026-09-30T16:00:00Z", "2026-10-31T16:00:00Z"},
		// February and year rollover.
		{"month", time.UTC, "2028-02-29T12:00:00Z", "2028-02-01T00:00:00Z", "2028-03-01T00:00:00Z"},
		{"month", time.UTC, "2026-12-31T23:59:59Z", "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		// DST: New York day of the spring-forward change is 23 hours long.
		{"day", ny, "2026-03-08T12:00:00Z", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
	}
	for _, c := range cases {
		s, e := calendarWindow(c.unit, c.loc, mustTime(t, c.now))
		if !s.Equal(mustTime(t, c.start)) || !e.Equal(mustTime(t, c.end)) {
			t.Errorf("%s %s @%s = [%s, %s), want [%s, %s)", c.unit, c.loc, c.now, s.Format(time.RFC3339),
				e.Format(time.RFC3339), c.start, c.end)
		}
	}
}

func TestPeriodWindow(t *testing.T) {
	start := mustTime(t, "2026-01-15T10:30:00Z")
	every := 30 * day
	for _, c := range []struct{ now, ws string }{
		{"2026-01-15T10:30:00Z", "2026-01-15T10:30:00Z"},
		{"2026-02-14T10:29:59Z", "2026-01-15T10:30:00Z"},
		{"2026-02-14T10:30:00Z", "2026-02-14T10:30:00Z"},
		{"2026-04-20T00:00:00Z", "2026-04-15T10:30:00Z"},
		{"2026-01-01T00:00:00Z", "2026-01-15T10:30:00Z"}, // before start: first period
	} {
		s, e := periodWindow(start, every, mustTime(t, c.now))
		if !s.Equal(mustTime(t, c.ws)) || e.Sub(s) != every {
			t.Errorf("period @%s = %s..%s, want start %s", c.now, s, e, c.ws)
		}
	}
}

func TestRollingState(t *testing.T) {
	r := rule(t, Rule{ID: "r", Meter: MeterRequests, Limit: "10", Window: Window{Kind: WindowRolling, Duration: "1h"}})
	now := mustTime(t, "2026-10-08T12:02:00Z")
	start := mustTime(t, "2026-10-01T00:00:00Z")
	if got := r.usageSince(start, now); !got.Equal(mustTime(t, "2026-10-08T11:02:00Z")) {
		t.Fatalf("since %s", got)
	}
	rows := []usageRow{
		{mustTime(t, "2026-10-08T11:00:00Z"), DecInt(100)}, // started before now-1h: not counted
		{mustTime(t, "2026-10-08T11:05:00Z"), DecInt(4)},
		{mustTime(t, "2026-10-08T11:30:00Z"), DecInt(3)},
		{mustTime(t, "2026-10-08T12:00:00Z"), DecInt(3)},
	}
	st := r.state(start, now, rows)
	if st.used.String() != "10" || !st.exceeded {
		t.Fatalf("used %s exceeded %v", st.used, st.exceeded)
	}
	if !st.resetsAt.Equal(mustTime(t, "2026-10-08T12:05:00Z")) {
		t.Fatalf("resetsAt %s", st.resetsAt)
	}
	// Dropping the 11:05 bucket (4) brings 10 → 6 < 10.
	if !st.retryAt.Equal(mustTime(t, "2026-10-08T12:05:00Z")) {
		t.Fatalf("retryAt %s", st.retryAt)
	}
	// With a higher total, two buckets must expire.
	rows[1].used = DecInt(1)
	rows[3].used = DecInt(8) // 1 + 3 + 8 = 12; drop 1 → 11, drop 3 → 8 < 10
	st = r.state(start, now, rows)
	if !st.retryAt.Equal(mustTime(t, "2026-10-08T12:30:00Z")) || !st.resetsAt.Equal(mustTime(t, "2026-10-08T12:05:00Z")) {
		t.Fatalf("retryAt %s resetsAt %s", st.retryAt, st.resetsAt)
	}
	// Exactly at bucket + duration the bucket no longer counts.
	st = r.state(start, mustTime(t, "2026-10-08T12:05:00Z"), rows)
	if st.used.String() != "11" {
		t.Fatalf("used at expiry %s", st.used)
	}
	if got := r.recordWindow(start, mustTime(t, "2026-10-08T12:04:59.5Z"), nil); !got.Equal(mustTime(t, "2026-10-08T12:00:00Z")) {
		t.Fatalf("bucket %s", got)
	}
	// Empty.
	st = r.state(start, now, nil)
	if st.used.Sign() != 0 || st.resetsAt != nil || st.exceeded || st.windowStart == nil {
		t.Fatalf("empty state %+v", st)
	}
}

func TestSessionState(t *testing.T) {
	r := rule(t, Rule{ID: "5h", Meter: MeterRequests, Limit: "2", Window: Window{Kind: WindowSession, Duration: "5h"}})
	start := mustTime(t, "2026-10-01T00:00:00Z")
	t0 := mustTime(t, "2026-10-08T09:17:42.123456Z")

	// Not started.
	st := r.state(start, t0, nil)
	if st.windowStart != nil || st.resetsAt != nil || st.used.Sign() != 0 || st.exceeded {
		t.Fatalf("not started: %+v", st)
	}
	// The first usage opens a session at the request time (second precision).
	ws := r.recordWindow(start, t0, nil)
	if !ws.Equal(mustTime(t, "2026-10-08T09:17:42Z")) {
		t.Fatalf("session start %s", ws)
	}
	rows := []usageRow{{ws, DecInt(2)}}
	// Later usage inside the session joins it.
	if got := r.recordWindow(start, ws.Add(4*time.Hour), rows); !got.Equal(ws) {
		t.Fatalf("join %s", got)
	}
	st = r.state(start, ws.Add(time.Hour), rows)
	if !st.exceeded || !st.windowStart.Equal(ws) || !st.resetsAt.Equal(ws.Add(5*time.Hour)) || !st.retryAt.Equal(ws.Add(5*time.Hour)) {
		t.Fatalf("active: %+v", st)
	}
	// At ws+5h the session has expired: window not started again.
	st = r.state(start, ws.Add(5*time.Hour), rows)
	if st.windowStart != nil || st.exceeded {
		t.Fatalf("expired: %+v", st)
	}
	next := ws.Add(5*time.Hour + 30*time.Second)
	if got := r.recordWindow(start, next, rows); !got.Equal(next) {
		t.Fatalf("new session %s", got)
	}
	// The newest live session wins.
	rows = append(rows, usageRow{next, DecInt(1)})
	st = r.state(start, next.Add(time.Minute), rows)
	if st.used.String() != "1" || !st.windowStart.Equal(next) {
		t.Fatalf("newest: %+v", st)
	}
}

func TestFixedWindows(t *testing.T) {
	start := mustTime(t, "2026-10-01T08:00:00Z")
	now := mustTime(t, "2026-10-08T12:00:00Z")
	life := rule(t, Rule{ID: "l", Meter: MeterRequests, Limit: "5", Window: Window{Kind: WindowLifetime}})
	st := life.state(start, now, []usageRow{{start, DecInt(5)}})
	if !st.exceeded || st.resetsAt != nil || st.retryAt != nil || !st.windowStart.Equal(start) {
		t.Fatalf("lifetime %+v", st)
	}
	if !life.recordWindow(start, now, nil).Equal(start) {
		t.Fatal("lifetime record window")
	}
	wk := rule(t, Rule{ID: "w", Meter: MeterTokensTotal, Limit: "1000",
		Window: Window{Kind: WindowCalendar, Unit: "week", Timezone: "Asia/Shanghai"}})
	ws := mustTime(t, "2026-10-04T16:00:00Z")
	st = wk.state(start, now, []usageRow{{ws, DecInt(400)}, {ws.Add(-7 * day), DecInt(5000)}})
	if st.used.String() != "400" || st.exceeded || !st.resetsAt.Equal(ws.Add(7*day)) {
		t.Fatalf("week %+v", st)
	}
	per := rule(t, Rule{ID: "p", Meter: MeterCharge, Limit: "20", Window: Window{Kind: WindowPeriod, Every: "7d"}})
	pws := mustTime(t, "2026-10-08T08:00:00Z")
	st = per.state(start, now, []usageRow{{pws, MustDec("20")}})
	if !st.exceeded || !st.retryAt.Equal(pws.Add(7*day)) {
		t.Fatalf("period %+v", st)
	}
}

func TestUnits(t *testing.T) {
	u := protocol.Usage{Input: 100, CacheRead: 50, CacheWrite: 10, Output: 40, Reasoning: 30}
	charge := money.MustParse("0.0123")
	cases := []struct {
		meter, model, want string
	}{
		{MeterRequests, "a", "1"},
		{MeterTokensInput, "a", "160"},
		{MeterTokensOutput, "a", "40"},
		{MeterTokensTotal, "a", "200"},
		{MeterCharge, "a", "0.0123"},
		{MeterRequests, "heavy", "5"},
		{MeterTokensTotal, "heavy", "1000"},
		{MeterTokensTotal, "light", "50"},
		{MeterCharge, "light", "0.003075"},
		{MeterTokensTotal, "free", "0"},
	}
	for _, c := range cases {
		r := rule(t, Rule{ID: "x", Meter: c.meter, Limit: "1", Window: Window{Kind: WindowLifetime},
			ModelWeights: map[string]string{"heavy": "5", "light": "0.25", "free": "0"}})
		if got := r.units(c.model, u, charge).String(); got != c.want {
			t.Errorf("%s/%s = %s, want %s", c.meter, c.model, got, c.want)
		}
	}
}

func TestValidateRules(t *testing.T) {
	details := map[string]any{}
	rules := []Rule{
		{ID: "ok", Meter: MeterRequests, Limit: "10", Window: Window{Kind: WindowSession, Duration: "5h"}},
		{ID: "ok", Meter: MeterRequests, Limit: "10", Window: Window{Kind: WindowLifetime}},
		{ID: "Bad ID", Meter: "tokens.cache", Limit: "1.5", Window: Window{Kind: WindowCalendar, Unit: "year", Timezone: "Mars/Base"}},
		{ID: "tok", Meter: MeterTokensTotal, Limit: "1.5", Window: Window{Kind: WindowRolling, Duration: "1m", Every: "1h"},
			ModelWeights: map[string]string{"m": "1001"}},
		{ID: "chg", Meter: MeterCharge, Limit: "0", Window: Window{Kind: WindowPeriod, Every: "400d"}, Models: []string{"a", "a"}},
		{ID: "neg", Meter: MeterCharge, Limit: "-1", Window: Window{Kind: "forever"}},
	}
	validateRules(rules, details)
	for _, k := range []string{"rules[1].id", "rules[2].id", "rules[2].meter", "rules[2].window.unit",
		"rules[2].window.timezone", "rules[3].limit", "rules[3].window.duration",
		"rules[3].window.every", "rules[3].modelWeights.m", "rules[4].limit", "rules[4].window.every",
		"rules[4].models", "rules[5].limit", "rules[5].window.kind"} {
		if _, ok := details[k]; !ok {
			t.Errorf("missing detail %s (got %v)", k, details)
		}
	}
	if _, ok := details["rules[0].id"]; ok {
		t.Errorf("first rule must be valid: %v", details)
	}
	details = map[string]any{}
	validateRules(nil, details)
	if details["rules"] == nil {
		t.Error("empty rules accepted")
	}
	// Calendar timezone defaults to UTC; charge limits may be fractional.
	ok := []Rule{{ID: "d", Meter: MeterCharge, Limit: "0.5", Window: Window{Kind: WindowCalendar, Unit: "day"}}}
	details = map[string]any{}
	validateRules(ok, details)
	if len(details) != 0 || ok[0].Window.Timezone != "UTC" {
		t.Fatalf("valid rule rejected: %v %+v", details, ok[0])
	}
}

// ---- decision logic without a database ----

type tsub struct {
	ends  time.Time
	rules []Rule
	usage map[string][]usageRow
	name  string
}

func runDecide(t *testing.T, now time.Time, subs ...tsub) *Decision {
	t.Helper()
	var loaded []*loadedSub
	usage := map[usageKey][]usageRow{}
	for i, s := range subs {
		sub := &Subscription{ID: uuid.New(), PlanName: s.name, StartsAt: now.Add(-10 * day), EndsAt: s.ends, Rules: s.rules}
		if sub.PlanName == "" {
			sub.PlanName = "P" + string(rune('A'+i))
		}
		ls, err := compileSub(sub)
		if err != nil {
			t.Fatal(err)
		}
		loaded = append(loaded, ls)
		for id, rows := range s.usage {
			usage[usageKey{sub.ID, id}] = rows
		}
	}
	return decide(loaded, usage, now)
}

func TestDecideLogic(t *testing.T) {
	now := mustTime(t, "2026-10-08T12:00:00Z")
	sess := Rule{ID: "s", Label: "5 小时窗口", Meter: MeterRequests, Limit: "2", Window: Window{Kind: WindowSession, Duration: "5h"}}
	life := Rule{ID: "l", Label: "试用", Meter: MeterRequests, Limit: "1", Window: Window{Kind: WindowLifetime}}
	sessStart := now.Add(-time.Hour)
	full := map[string][]usageRow{"s": {{sessStart, DecInt(2)}}}

	// Blocked: retry when the session ends.
	d := runDecide(t, now, tsub{ends: now.Add(30 * day), rules: []Rule{sess}, usage: full, name: "Pro"})
	if d.Blocked == nil || d.Blocked.Code != CodeQuotaExceeded || d.Blocked.RetryAfter != 4*time.Hour {
		t.Fatalf("blocked: %+v", d.Blocked)
	}
	if d.Blocked.Message != "套餐「Pro」的「5 小时窗口」额度已用完，将于 16:00 UTC 重置" {
		t.Fatalf("message %q", d.Blocked.Message)
	}
	// Two exceeded rules in one subscription: wait for the later reset.
	dayRule := Rule{ID: "d", Meter: MeterRequests, Limit: "2", Window: Window{Kind: WindowCalendar, Unit: "day"}}
	d = runDecide(t, now, tsub{ends: now.Add(30 * day), rules: []Rule{sess, dayRule},
		usage: map[string][]usageRow{"s": full["s"], "d": {{mustTime(t, "2026-10-08T00:00:00Z"), DecInt(2)}}}})
	if d.Blocked == nil || d.Blocked.RetryAfter != 12*time.Hour || !strings.Contains(d.Blocked.Message, "10-09 00:00 UTC") {
		t.Fatalf("two rules: %+v", d.Blocked)
	}
	// Lifetime exhausted.
	d = runDecide(t, now, tsub{ends: now.Add(30 * day), rules: []Rule{life},
		usage: map[string][]usageRow{"l": {{now.Add(-10 * day), DecInt(1)}}}})
	if d.Blocked == nil || d.Blocked.Code != CodeQuotaExhausted || d.Blocked.RetryAfter != 0 {
		t.Fatalf("lifetime: %+v", d.Blocked)
	}
	// A reset after the subscription ends does not help → exhausted.
	d = runDecide(t, now, tsub{ends: now.Add(time.Hour), rules: []Rule{sess}, usage: full})
	if d.Blocked == nil || d.Blocked.Code != CodeQuotaExhausted {
		t.Fatalf("ends before reset: %+v", d.Blocked)
	}
	// Earliest-ending admitting subscription wins; exhausted ones are skipped.
	d = runDecide(t, now,
		tsub{ends: now.Add(2 * day), rules: []Rule{sess}, usage: full},
		tsub{ends: now.Add(3 * day), rules: []Rule{sess}},
		tsub{ends: now.Add(4 * day), rules: []Rule{sess}})
	if d.SubscriptionID == nil {
		t.Fatalf("expected admission: %+v", d)
	}
	// Retry-after is the earliest among subscriptions.
	d = runDecide(t, now,
		tsub{ends: now.Add(30 * day), rules: []Rule{sess}, usage: full},
		tsub{ends: now.Add(30 * day), rules: []Rule{life}, usage: map[string][]usageRow{"l": {{now.Add(-10 * day), DecInt(1)}}}})
	if d.Blocked == nil || d.Blocked.Code != CodeQuotaExceeded || d.Blocked.RetryAfter != 4*time.Hour {
		t.Fatalf("mixed: %+v", d.Blocked)
	}
	// Retry-after rounds up to whole seconds.
	d = runDecide(t, now.Add(500*time.Millisecond), tsub{ends: now.Add(30 * day), rules: []Rule{sess}, usage: full})
	if d.Blocked.RetryAfter != 4*time.Hour {
		t.Fatalf("rounding: %v", d.Blocked.RetryAfter)
	}
}
