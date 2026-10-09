// Package netguard builds HTTP clients for upstream calls that refuse to
// connect to internal addresses (SSRF protection). The check runs in the
// dialer's Control hook, i.e. on the already-resolved IP, so DNS rebinding
// cannot bypass it.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrBlocked is returned (wrapped) when a connection target is not allowed.
var ErrBlocked = errors.New("netguard: destination address not allowed")

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001:db8::/32",
		"fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsPublic reports whether ip is a globally routable unicast address.
func IsPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBlocked, address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !IsPublic(ip) {
		return fmt.Errorf("%w: %s", ErrBlocked, host)
	}
	return nil
}

// Options configure a client.
type Options struct {
	// AllowPrivate disables the address check (trusted admin channels only).
	AllowPrivate bool
	// Proxy, when set, routes all upstream traffic through this proxy. The proxy
	// itself is trusted; targets are then checked by resolving the hostname
	// before the request (best effort, see CheckHost).
	Proxy *url.URL
}

// NewTransport returns a transport tuned for long-lived streaming upstream calls.
// Environment proxy variables are deliberately ignored.
func NewTransport(o Options) *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !o.AllowPrivate && o.Proxy == nil {
		d.Control = control
	}
	t := &http.Transport{
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		// Response header timeouts are enforced per request by the caller.
	}
	if o.Proxy != nil {
		t.Proxy = http.ProxyURL(o.Proxy)
	}
	return t
}

// CheckHost resolves host and rejects it if any address is not public. Used
// when a proxy is configured (the dial-time check then only sees the proxy).
func CheckHost(ctx context.Context, host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !IsPublic(ip) {
			return fmt.Errorf("%w: %s", ErrBlocked, host)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if !IsPublic(a) {
			return fmt.Errorf("%w: %s resolves to %s", ErrBlocked, host, a)
		}
	}
	return nil
}

// NoRedirect makes clients return 3xx responses instead of following them, so
// a redirect cannot carry credentials or requests to an unchecked host.
func NoRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
