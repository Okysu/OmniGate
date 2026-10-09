package subscription

import (
	"errors"
	"math/big"
	"strings"
)

// decScale is the number of fractional digits kept by Dec (same as money and
// the numeric(38,9) columns).
const decScale = 9

var (
	bigUnit       = big.NewInt(1_000_000_000)
	errDecSyntax  = errors.New("不是合法的十进制数")
	errDecPrecise = errors.New("最多 9 位小数")
	errDecRange   = errors.New("数值过大")
)

// maxDecDigits bounds parsed input to what numeric(38,9) can store.
const maxDecDigits = 38

// Dec is an exact decimal with 9 fractional digits, held as a big integer count
// of 1e-9 units. The zero value is 0. Floats are never used for quota math.
type Dec struct{ n *big.Int }

func (d Dec) int() *big.Int {
	if d.n == nil {
		return new(big.Int)
	}
	return d.n
}

// DecInt returns the integer v.
func DecInt(v int64) Dec { return Dec{new(big.Int).Mul(big.NewInt(v), bigUnit)} }

// DecNano returns v × 1e-9 (e.g. a money.Amount).
func DecNano(v int64) Dec { return Dec{big.NewInt(v)} }

// ParseDec parses a plain decimal such as "12", "0.5" or "-3.25" (no exponent,
// at most 9 fractional digits).
func ParseDec(s string) (Dec, error) {
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" || (hasDot && frac == "") {
		return Dec{}, errDecSyntax
	}
	if len(frac) > decScale {
		return Dec{}, errDecPrecise
	}
	digits := intPart + frac + strings.Repeat("0", decScale-len(frac))
	for _, c := range digits {
		if c < '0' || c > '9' {
			return Dec{}, errDecSyntax
		}
	}
	if len(strings.TrimLeft(digits, "0")) > maxDecDigits {
		return Dec{}, errDecRange
	}
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Dec{}, errDecSyntax
	}
	if neg {
		n.Neg(n)
	}
	return Dec{n}, nil
}

// MustDec is ParseDec for constants and tests.
func MustDec(s string) Dec {
	d, err := ParseDec(s)
	if err != nil {
		panic(err)
	}
	return d
}

// String renders the decimal with trailing fractional zeros trimmed.
func (d Dec) String() string {
	n := d.int()
	neg := n.Sign() < 0
	a := new(big.Int).Abs(n)
	q, r := new(big.Int).QuoRem(a, bigUnit, new(big.Int))
	s := q.String()
	if r.Sign() != 0 {
		f := r.String()
		f = strings.Repeat("0", decScale-len(f)) + f
		s += "." + strings.TrimRight(f, "0")
	}
	if neg {
		return "-" + s
	}
	return s
}

func (d Dec) Add(o Dec) Dec { return Dec{new(big.Int).Add(d.int(), o.int())} }
func (d Dec) Sub(o Dec) Dec { return Dec{new(big.Int).Sub(d.int(), o.int())} }
func (d Dec) Cmp(o Dec) int { return d.int().Cmp(o.int()) }
func (d Dec) Sign() int     { return d.int().Sign() }

// IsInt reports whether d has no fractional part.
func (d Dec) IsInt() bool { return new(big.Int).Rem(d.int(), bigUnit).Sign() == 0 }

// Mul returns d × o rounded half away from zero to 9 fractional digits.
func (d Dec) Mul(o Dec) Dec {
	p := new(big.Int).Mul(d.int(), o.int())
	q, r := new(big.Int).QuoRem(p, bigUnit, new(big.Int))
	if new(big.Int).Mul(new(big.Int).Abs(r), big.NewInt(2)).Cmp(bigUnit) >= 0 {
		if p.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return Dec{q}
}

// maxDec returns the larger of a and b.
func maxDec(a, b Dec) Dec {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}
