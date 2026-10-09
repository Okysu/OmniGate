package netguard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIsPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.20.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.100.100.200": false, "::1": false, "fd00::1": false,
		"fe80::1": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false, "0.0.0.0": false,
	} {
		if got := IsPublic(netip.MustParseAddr(addr)); got != want {
			t.Errorf("IsPublic(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestTransportBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()

	guarded := &http.Client{Transport: NewTransport(Options{})}
	if _, err := guarded.Get(srv.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("guarded client reached loopback: %v", err)
	}
	open := &http.Client{Transport: NewTransport(Options{AllowPrivate: true})}
	resp, err := open.Get(srv.URL)
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("open client failed: %v", err)
	}
	resp.Body.Close()
}

func TestCheckHost(t *testing.T) {
	if err := CheckHost(context.Background(), "127.0.0.1"); !errors.Is(err, ErrBlocked) {
		t.Fatal("loopback literal must be blocked")
	}
	if err := CheckHost(context.Background(), "localhost"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("localhost must be blocked: %v", err)
	}
}
