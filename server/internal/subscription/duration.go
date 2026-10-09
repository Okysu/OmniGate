package subscription

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Duration bounds from contracts/phase3-api.md §1.
const (
	minWindowDuration = 5 * time.Minute // rolling, session
	maxWindowDuration = 31 * day
	minPeriod         = time.Hour // period window, plan duration
	maxPeriod         = 366 * day

	day = 24 * time.Hour
)

var errDurationSyntax = errors.New("格式应为 <数字><单位>，单位为 m、h 或 d，例如 5h、30d")

// ParseDuration parses "<n>m|h|d" (minutes, hours, days of 24h). n is a positive
// integer without sign, leading zeros or a fraction.
func ParseDuration(s string) (time.Duration, error) {
	if len(s) < 2 || len(s) > 8 {
		return 0, errDurationSyntax
	}
	num, unit := s[:len(s)-1], s[len(s)-1]
	if num[0] == '0' {
		return 0, errDurationSyntax
	}
	for _, c := range num {
		if c < '0' || c > '9' {
			return 0, errDurationSyntax
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, errDurationSyntax
	}
	var u time.Duration
	switch unit {
	case 'm':
		u = time.Minute
	case 'h':
		u = time.Hour
	case 'd':
		u = day
	default:
		return 0, errDurationSyntax
	}
	if time.Duration(n) > math.MaxInt64/u {
		return 0, errDurationSyntax
	}
	return time.Duration(n) * u, nil
}

// parseDurationIn parses s and checks lo ≤ d ≤ hi.
func parseDurationIn(s string, lo, hi time.Duration, loS, hiS string) (time.Duration, error) {
	d, err := ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < lo || d > hi {
		return 0, fmt.Errorf("必须在 %s 到 %s 之间", loS, hiS)
	}
	return d, nil
}

// ParseWindowDuration parses a rolling/session duration (5m–31d).
func ParseWindowDuration(s string) (time.Duration, error) {
	return parseDurationIn(s, minWindowDuration, maxWindowDuration, "5m", "31d")
}

// ParsePeriod parses a period window or plan duration (1h–366d).
func ParsePeriod(s string) (time.Duration, error) {
	return parseDurationIn(s, minPeriod, maxPeriod, "1h", "366d")
}
