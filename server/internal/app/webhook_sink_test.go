package app_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"omnigate/internal/notify"
)

type sinkReq struct {
	header http.Header
	body   []byte
}

type webhookSink struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []sinkReq
}

func newWebhookSink(t *testing.T) *webhookSink {
	s := &webhookSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, sinkReq{r.Header.Clone(), b})
		s.mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *webhookSink) wait(t *testing.T, n int) []sinkReq {
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		got := append([]sinkReq(nil), s.reqs...)
		s.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook sink got %d requests, want %d", len(got), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func signHMAC(secret string, body []byte) string { return notify.Sign(secret, body) }
