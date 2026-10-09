package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"omnigate/internal/money"
	"omnigate/internal/platform/tzcache"
)

// Multiplier is a non-negative decimal factor in nano units (One = ×1), used
// for user-group price multipliers and time-of-day schedules
// (docs/contracts/phase8-api.md §1.1, §3).
type Multiplier int64

// One is the neutral multiplier.
const One Multiplier = 1_000_000_000

// Upper bounds of the multipliers (§1, §3).
const (
	MaxGroupMultiplier    Multiplier = 100 * One
	MaxScheduleMultiplier Multiplier = 10 * One
)

// ParseMultiplier parses a decimal string in [0, max] with at most 9 decimals.
func ParseMultiplier(s string, max Multiplier) (Multiplier, error) {
	a, err := money.Parse(strings.TrimSpace(s))
	if err != nil || a < 0 || Multiplier(a) > max {
		return 0, fmt.Errorf("必须是 0–%s 之间的十进制数，最多 9 位小数", max)
	}
	return Multiplier(a), nil
}

func (m Multiplier) String() string { return money.Amount(m).String() }

var bigOne = big.NewInt(int64(One))

// applyFactors returns a × f1 × f2 × … rounded half away from zero (exact
// big-integer arithmetic; one rounding step).
func applyFactors(a money.Amount, factors ...Multiplier) (money.Amount, error) {
	n := big.NewInt(int64(a))
	d := big.NewInt(1)
	for _, f := range factors {
		if f == One {
			continue
		}
		n.Mul(n, big.NewInt(int64(f)))
		d.Mul(d, bigOne)
	}
	if d.Cmp(big.NewInt(1)) == 0 {
		return a, nil
	}
	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	if new(big.Int).Mul(new(big.Int).Abs(r), big.NewInt(2)).Cmp(d) >= 0 {
		if n.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, money.ErrOverflow
	}
	return money.Amount(q.Int64()), nil
}

// Apply returns a × m rounded half away from zero.
func (m Multiplier) Apply(a money.Amount) (money.Amount, error) { return applyFactors(a, m) }

// Product renders the exact product of the factors as a decimal string
// without trailing zeros (e.g. 0.5 × 0.8 = "0.4"): the priceMultiplier of a
// request log.
func Product(factors ...Multiplier) string {
	n := big.NewInt(1)
	scale := 0
	for _, f := range factors {
		n.Mul(n, big.NewInt(int64(f)))
		scale += 9
	}
	if scale == 0 {
		return "1"
	}
	s := n.String()
	if len(s) <= scale {
		s = strings.Repeat("0", scale-len(s)+1) + s
	}
	intPart, frac := s[:len(s)-scale], strings.TrimRight(s[len(s)-scale:], "0")
	if frac == "" {
		return intPart
	}
	return intPart + "." + frac
}

// ScheduleSlot is one time-of-day window of a price version (§3). Days are
// weekdays (0 = Sunday … 6 = Saturday; empty = every day) on which the slot
// starts; Start is inclusive, End exclusive; Start > End crosses midnight
// (the part after midnight belongs to the next day). End may be "24:00".
type ScheduleSlot struct {
	Days       []int  `json:"days"`
	Start      string `json:"start"`
	End        string `json:"end"`
	Multiplier string `json:"multiplier"`

	start, end int // minutes since midnight
	mult       Multiplier
}

const maxSlots = 48

// parseClock parses "HH:MM" (00:00–23:59; "24:00" only when allow24).
func parseClock(s string, allow24 bool) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for i, c := range s {
		if i == 2 {
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if m > 59 || h > 24 || (h == 24 && (m != 0 || !allow24)) {
		return 0, false
	}
	return h*60 + m, true
}

// compileSchedule validates slots and fills their parsed fields. An empty
// list compiles to nil (no schedule). Overlapping slots are rejected.
func compileSchedule(in []ScheduleSlot) ([]ScheduleSlot, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxSlots {
		return nil, fmt.Errorf("最多 %d 个时段", maxSlots)
	}
	out := make([]ScheduleSlot, len(in))
	var week [7 * 1440]int8 // minute of the week → slot index + 1
	for i, s := range in {
		at := fmt.Sprintf("第 %d 个时段：", i+1)
		days := slices.Clone(s.Days)
		if days == nil {
			days = []int{}
		}
		slices.Sort(days)
		days = slices.Compact(days)
		for _, d := range days {
			if d < 0 || d > 6 {
				return nil, errors.New(at + "days 只能包含 0（周日）到 6（周六）")
			}
		}
		start, ok1 := parseClock(s.Start, false)
		end, ok2 := parseClock(s.End, true)
		if !ok1 || !ok2 {
			return nil, errors.New(at + "start / end 必须是 HH:MM 格式（end 可以是 24:00）")
		}
		if start == end {
			return nil, errors.New(at + "start 与 end 不能相同")
		}
		if start > end && end == 1440 {
			return nil, errors.New(at + "跨零点的时段 end 不能是 24:00")
		}
		mult, err := ParseMultiplier(s.Multiplier, MaxScheduleMultiplier)
		if err != nil {
			return nil, errors.New(at + "multiplier " + err.Error())
		}
		slot := ScheduleSlot{Days: days, Start: s.Start, End: s.End, Multiplier: mult.String(), start: start, end: end, mult: mult}
		active := days
		if len(active) == 0 {
			active = []int{0, 1, 2, 3, 4, 5, 6}
		}
		for _, d := range active {
			mark := func(from, to, day int) error {
				for m := from; m < to; m++ {
					idx := (day%7)*1440 + m
					if week[idx] != 0 {
						return fmt.Errorf("%s与第 %d 个时段重叠", at, week[idx])
					}
					week[idx] = int8(i + 1)
				}
				return nil
			}
			if start < end {
				err = mark(start, end, d)
			} else {
				if err = mark(start, 1440, d); err == nil {
					err = mark(0, end, d+1)
				}
			}
			if err != nil {
				return nil, err
			}
		}
		out[i] = slot
	}
	return out, nil
}

// decodeSchedule parses a stored schedule (invalid documents yield none).
func decodeSchedule(raw []byte) []ScheduleSlot {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var in []ScheduleSlot
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	out, err := compileSchedule(in)
	if err != nil {
		return nil
	}
	return out
}

func (s *ScheduleSlot) hasDay(d int) bool { return len(s.Days) == 0 || slices.Contains(s.Days, d) }

// matches reports whether the slot covers local weekday d, minute m.
func (s *ScheduleSlot) matches(d, m int) bool {
	if s.start < s.end {
		return s.hasDay(d) && m >= s.start && m < s.end
	}
	return (s.hasDay(d) && m >= s.start) || (s.hasDay((d+6)%7) && m < s.end)
}

// ScheduleMultiplier returns the multiplier of the first slot covering at in
// the version's schedule timezone (One when none matches or no schedule).
func (p *Price) ScheduleMultiplier(at time.Time) Multiplier {
	if p == nil || len(p.Schedule) == 0 {
		return One
	}
	t := at.In(tzcache.MustLoad(p.ScheduleTimezone))
	d, m := int(t.Weekday()), t.Hour()*60+t.Minute()
	for i := range p.Schedule {
		if p.Schedule[i].matches(d, m) {
			return p.Schedule[i].mult
		}
	}
	return One
}
