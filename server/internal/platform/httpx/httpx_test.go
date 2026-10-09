package httpx

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIPPrefix(t *testing.T) {
	cases := map[string]string{
		"203.0.113.77":          "203.0.113.0/24",
		"::ffff:203.0.113.77":   "203.0.113.0/24",
		"2001:db8:abcd:1234::1": "2001:db8:abcd::/48",
	}
	for in, want := range cases {
		if got := IPPrefix(netip.MustParseAddr(in)); got != want {
			t.Errorf("IPPrefix(%s) = %s, want %s", in, got, want)
		}
	}
	if IPPrefix(netip.Addr{}) != "" {
		t.Error("invalid addr must give empty prefix")
	}
}

func TestResolveClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	r := httptest.NewRequest("GET", "/", nil)

	r.RemoteAddr = "198.51.100.1:1234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := resolveClientIP(r, trusted); got.String() != "198.51.100.1" {
		t.Errorf("untrusted peer must not honor XFF, got %s", got)
	}

	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4, 10.0.0.9")
	if got := resolveClientIP(r, trusted); got.String() != "1.2.3.4" {
		t.Errorf("expected right-most untrusted hop 1.2.3.4, got %s", got)
	}
}
