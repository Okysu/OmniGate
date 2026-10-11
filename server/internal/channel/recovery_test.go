package channel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestClassifyProbe(t *testing.T) {
	cases := []struct {
		status int
		err    error
		want   probeOutcome
	}{
		{200, nil, probeHealthy},
		{0, errors.New("dial"), probeUnhealthy},
		{401, nil, probeUnhealthy},
		{429, nil, probeUnhealthy},
		{502, nil, probeUnhealthy},
		{404, nil, probeUnknown},
		{400, nil, probeUnknown},
	}
	for _, c := range cases {
		if got, _ := classifyProbe(c.status, nil, c.err); got != c.want {
			t.Errorf("classifyProbe(%d, %v) = %v, want %v", c.status, c.err, got, c.want)
		}
	}
}

// An open circuit is closed by the prober once the upstream answers again,
// without any user request; while the upstream still fails it stays open.
func TestRecoveryProber(t *testing.T) {
	var healthy atomic.Bool
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/models" || !healthy.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer up.Close()
	old := recoveryTick
	recoveryTick = 10 * time.Millisecond
	defer func() { recoveryTick = old }()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := NewRegistry(nil, log, RegistryOptions{})
	reg.Breaker = NewBreaker(1, time.Minute, 20*time.Millisecond)
	rt := &Runtime{Channel: Channel{ID: uuid.New(), Name: "up", Type: TypeOpenAI, BaseURL: up.URL + "/v1"}, APIKey: "k", AllowPrivate: true}
	reg.all, reg.byID = []*Runtime{rt}, map[uuid.UUID]*Runtime{rt.ID: rt}
	s := &Service{reg: reg, userAgent: "test"}
	reg.Breaker.Failure(rt.ID, "boom")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RunRecoveryProber(ctx, log); close(done) }()
	defer func() { cancel(); <-done }()

	wait := func(cond func() bool) bool {
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if cond() {
				return true
			}
		}
		return false
	}
	if !wait(func() bool { return hits.Load() >= 2 }) {
		t.Fatal("prober never probed the open channel")
	}
	if st := reg.Breaker.State(rt.ID); st == "closed" {
		t.Fatal("a failing upstream must keep the circuit open")
	}
	healthy.Store(true)
	if !wait(func() bool { return reg.Breaker.State(rt.ID) == "closed" }) {
		t.Fatalf("circuit not recovered: %s", reg.Breaker.State(rt.ID))
	}
	if h := reg.Breaker.Health(rt.ID); h.State != "healthy" {
		t.Fatalf("health = %+v", h)
	}
}
