// Package money implements fixed-point monetary amounts. Floats are never used
// for money in OmniGate.
//
// Amount is an int64 count of nano-units (1e-9) of the deployment settlement
// currency, giving ±9.2 billion major units of range: ample for a single
// deployment's ledger while keeping sub-cent precision for per-token pricing.
// Aggregations across many rows should be summed in PostgreSQL NUMERIC.
package money

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// Scale is the number of decimal places stored.
const Scale = 9

const unit = 1_000_000_000

type Amount int64

var (
	ErrSyntax   = errors.New("money: invalid decimal syntax")
	ErrPrecise  = errors.New("money: more than 9 decimal places")
	ErrOverflow = errors.New("money: overflow")
)

// Parse parses a plain decimal string such as "12.34" or "-0.000002".
func Parse(s string) (Amount, error) {
	s = strings.TrimSpace(s)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	} else if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" && (!hasDot || frac == "") {
		return 0, ErrSyntax
	}
	if len(frac) > Scale {
		return 0, ErrPrecise
	}
	digits := intPart + frac + strings.Repeat("0", Scale-len(frac))
	var v int64
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, ErrSyntax
		}
		if v > (math.MaxInt64-int64(r-'0'))/10 {
			return 0, ErrOverflow
		}
		v = v*10 + int64(r-'0')
	}
	if neg {
		v = -v
	}
	return Amount(v), nil
}

// MustParse is Parse for constants in code and tests.
func MustParse(s string) Amount {
	a, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return a
}

// String renders the full-precision decimal with trailing zeros trimmed.
func (a Amount) String() string {
	neg := a < 0
	u := uint64(a)
	if neg {
		u = uint64(-(a + 1)) + 1 // avoids overflow on MinInt64
	}
	s := fmt.Sprintf("%d.%09d", u/unit, u%unit)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if neg {
		return "-" + s
	}
	return s
}

// Format renders with exactly `decimals` places, rounding half away from zero.
func (a Amount) Format(decimals int) string {
	if decimals < 0 || decimals > Scale {
		decimals = Scale
	}
	r := a.Round(decimals)
	neg := r < 0
	u := uint64(r)
	if neg {
		u = uint64(-(r + 1)) + 1
	}
	s := fmt.Sprintf("%d", u/unit)
	if decimals > 0 {
		s += "." + fmt.Sprintf("%09d", u%unit)[:decimals]
	}
	if neg && r != 0 {
		return "-" + s
	}
	return s
}

// Round rounds to the given number of decimal places, half away from zero.
func (a Amount) Round(decimals int) Amount {
	if decimals >= Scale {
		return a
	}
	step := int64(math.Pow10(Scale - decimals))
	q, r := int64(a)/step, int64(a)%step
	if r*2 >= step {
		q++
	} else if r*2 <= -step {
		q--
	}
	return Amount(q * step)
}

func (a Amount) Add(b Amount) (Amount, error) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, ErrOverflow
	}
	return s, nil
}

// MulDiv returns a*num/den rounded half away from zero, computed exactly.
// Typical use: cost = PerMillion(price).MulDiv(tokens, 1_000_000).
func (a Amount) MulDiv(num, den int64) (Amount, error) {
	if den == 0 {
		return 0, errors.New("money: division by zero")
	}
	n := new(big.Int).Mul(big.NewInt(int64(a)), big.NewInt(num))
	d := big.NewInt(den)
	if d.Sign() < 0 {
		n.Neg(n)
		d.Neg(d)
	}
	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	r2 := new(big.Int).Mul(new(big.Int).Abs(r), big.NewInt(2))
	if r2.Cmp(d) >= 0 {
		if n.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, ErrOverflow
	}
	return Amount(q.Int64()), nil
}

// TokenCost prices `tokens` at `perMillion` (price per 1M tokens).
func TokenCost(perMillion Amount, tokens int64) (Amount, error) {
	return perMillion.MulDiv(tokens, 1_000_000)
}
