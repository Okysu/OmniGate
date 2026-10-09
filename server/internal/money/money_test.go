package money

import (
	"errors"
	"math"
	"testing"
)

func TestParseAndString(t *testing.T) {
	cases := map[string]string{
		"0":            "0",
		"1":            "1",
		"12.34":        "12.34",
		"-0.000002":    "-0.000002",
		"0.000000001":  "0.000000001",
		".5":           "0.5",
		"+3.10":        "3.1",
		"9223372036.8": "9223372036.8",
	}
	for in, want := range cases {
		a, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got := a.String(); got != want {
			t.Errorf("Parse(%q).String() = %q, want %q", in, got, want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for in, want := range map[string]error{
		"":                ErrSyntax,
		"abc":             ErrSyntax,
		"1.2.3":           ErrSyntax,
		"1.0000000001":    ErrPrecise,
		"99999999999":     ErrOverflow,
		"-":               ErrSyntax,
		"1e5":             ErrSyntax,
		"9223372036.9999": ErrOverflow,
	} {
		if _, err := Parse(in); !errors.Is(err, want) {
			t.Errorf("Parse(%q) err = %v, want %v", in, err, want)
		}
	}
}

func TestFormatRounding(t *testing.T) {
	cases := []struct {
		in   string
		dec  int
		want string
	}{
		{"1.005", 2, "1.01"},
		{"1.004999999", 2, "1.00"},
		{"-1.005", 2, "-1.01"},
		{"-0.001", 2, "0.00"},
		{"12", 2, "12.00"},
		{"12.5", 0, "13"},
	}
	for _, c := range cases {
		if got := MustParse(c.in).Format(c.dec); got != c.want {
			t.Errorf("Format(%s, %d) = %s, want %s", c.in, c.dec, got, c.want)
		}
	}
}

func TestTokenCost(t *testing.T) {
	// $0.15 per 1M tokens * 10 tokens = $0.0000015
	got, err := TokenCost(MustParse("0.15"), 10)
	if err != nil || got != MustParse("0.0000015") {
		t.Fatalf("TokenCost = %v, %v", got, err)
	}
	// Rounds half away from zero at nano precision: $0.000001 per 1M * 1 token = 1e-12 -> 0
	got, _ = TokenCost(MustParse("0.000001"), 1)
	if got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}
	// Large token counts don't overflow intermediate math.
	got, err = TokenCost(MustParse("75"), 2_000_000_000)
	if err != nil || got != MustParse("150000") {
		t.Fatalf("large TokenCost = %v, %v", got, err)
	}
}

func TestAddOverflow(t *testing.T) {
	if _, err := Amount(math.MaxInt64).Add(1); !errors.Is(err, ErrOverflow) {
		t.Fatal("expected overflow")
	}
	if s, err := MustParse("1.5").Add(MustParse("-2")); err != nil || s.String() != "-0.5" {
		t.Fatalf("Add = %v, %v", s, err)
	}
}

func TestMinInt64String(t *testing.T) {
	if got := Amount(math.MinInt64).String(); got != "-9223372036.854775808" {
		t.Fatalf("got %s", got)
	}
}
