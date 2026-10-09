package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"omnigate/internal/app"
)

// A request still streaming when shutdown starts is cut after the grace
// period, but its settlement and request log are still written before the
// process stops, and a graceful stop returns nil (exit code 0).
func TestGracefulShutdownKeepsInFlightLogs(t *testing.T) {
	e := setupGateway(t)
	up := newFakeUpstream(t)
	e.channel(e.admin, map[string]any{"name": "oa", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	// A second instance on the same database, with a short grace period.
	cfg := *e.cfg
	cfg.MetricsAddr = "off" // :9090 may be taken (e.g. by a dev server)
	b, err := app.New(context.Background(), &cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), e.pool, app.Options{ShutdownGrace: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Serve(ctx, ln) }()
	for i := 0; ; i++ {
		if resp, err := http.Get(base + "/readyz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if i > 100 {
			resp, err := http.Get(base + "/readyz")
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				t.Fatalf("second instance never became ready: %d %s", resp.StatusCode, body)
			}
			t.Fatalf("second instance never became ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // channel registry load

	up.setMode("block") // the stream stays open until the grace period cuts it
	resp := gwPost(t, context.Background(), base, "/v1/chat/completions", key,
		`{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	buf := make([]byte, 64)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	stop() // SIGTERM
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("graceful shutdown returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish")
	}

	// The cut stream was logged by the instance that served it.
	logs := e.mustDo(e.admin, http.MethodGet, "/api/logs?pageSize=5&model=m1", nil, 200)
	if n := len(logs["items"].([]any)); n != 1 {
		t.Fatalf("request logs after shutdown = %d, want 1", n)
	}
}
