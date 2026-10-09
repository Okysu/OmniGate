package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	for _, addr := range []string{":" + port, "0.0.0.0:" + port, "127.0.0.1:" + port} {
		if err := healthcheck(addr); err != nil {
			t.Fatalf("healthcheck(%q) = %v", addr, err)
		}
	}
	status = http.StatusServiceUnavailable
	if err := healthcheck(":" + port); err == nil {
		t.Fatal("non-200 must fail")
	}
	if err := healthcheck("not-an-addr"); err == nil {
		t.Fatal("bad address must fail")
	}
}
