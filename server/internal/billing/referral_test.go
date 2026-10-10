package billing

import (
	"testing"

	"omnigate/internal/money"
)

func TestRebateAmount(t *testing.T) {
	m := money.MustParse
	if got := RebateAmount(m("100"), 1000); got != m("10") {
		t.Fatalf("10%% of 100 = %s", got)
	}
	if got := RebateAmount(m("33.33"), 1250); got != m("4.16625") {
		t.Fatalf("12.5%% of 33.33 = %s", got)
	}
	if RebateAmount(m("100"), 0) != 0 || RebateAmount(0, 1000) != 0 {
		t.Fatal("zero rate or recharge must give no rebate")
	}
}

func TestMaskName(t *testing.T) {
	for in, want := range map[string]string{"": "*", "A": "A*", "张三": "张*", "Alice": "A***e", "Christopher": "C****r", " bob ": "b*b"} {
		if got := MaskName(in); got != want {
			t.Errorf("MaskName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInviteCodes(t *testing.T) {
	c, err := newInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := NormalizeInviteCode(" " + c + " "); !ok || n != c {
		t.Fatalf("code %q does not round-trip: %q %v", c, n, ok)
	}
	if n, ok := NormalizeInviteCode("abcdefgh"); !ok || n != "ABCDEFGH" {
		t.Fatalf("lower-case code = %q %v", n, ok)
	}
	for _, bad := range []string{"", "ABC", "ABCDEFG0", "ABCDEFGHJ", "ABCD-EFG"} {
		if _, ok := NormalizeInviteCode(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
