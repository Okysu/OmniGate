package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Active recovery of open circuits. The breaker alone recovers passively: once
// the cooldown has elapsed it lets one real request through as the probe, so a
// channel nobody routes to stays "open" forever, and recovering costs a user
// request a failed attempt. The recovery prober takes that half-open probe slot
// itself and checks the upstream out of band — the model list for openai /
// anthropic channels, the plugin's health.check (else models.list) for
// custom-protocol channels, the same checks as the connectivity test.

// recoveryTick is a variable so tests can speed the prober up.
var recoveryTick = 15 * time.Second

const (
	// recoveryUnknownBackoff is how long a channel whose check is inconclusive
	// (e.g. the upstream has no model list endpoint) is left to passive
	// recovery before it is checked again.
	recoveryUnknownBackoff = 5 * time.Minute
	recoveryConcurrency    = 4
)

type probeOutcome int

const (
	probeHealthy probeOutcome = iota
	probeUnhealthy
	probeUnknown // says nothing about health; the probe slot is released
)

// RunRecoveryProber probes channels whose circuit is half-open until ctx ends.
func (s *Service) RunRecoveryProber(ctx context.Context, log *slog.Logger) {
	t := time.NewTicker(recoveryTick)
	defer t.Stop()
	sem := make(chan struct{}, recoveryConcurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	var mu sync.Mutex
	skipUntil := map[uuid.UUID]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		for _, rt := range s.reg.All() {
			mu.Lock()
			skip := now.Before(skipUntil[rt.ID])
			mu.Unlock()
			if skip || s.reg.Breaker.State(rt.ID) != "half_open" {
				continue
			}
			// Allow takes the single half-open probe slot, so real traffic
			// keeps skipping the channel while it is being checked.
			if !s.reg.Breaker.Allow(rt.ID) {
				continue
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				s.reg.Breaker.Release(rt.ID)
				return
			}
			wg.Add(1)
			go func(rt *Runtime) {
				defer func() { <-sem; wg.Done() }()
				outcome, msg := s.recoveryProbe(ctx, rt)
				switch outcome {
				case probeHealthy:
					s.reg.Breaker.Success(rt.ID)
					log.Info("channel recovered by active probe", "channel_id", rt.ID, "channel", rt.Name)
				case probeUnhealthy:
					s.reg.Breaker.Failure(rt.ID, msg)
				default:
					s.reg.Breaker.Release(rt.ID)
					mu.Lock()
					skipUntil[rt.ID] = time.Now().Add(recoveryUnknownBackoff)
					mu.Unlock()
				}
			}(rt)
		}
	}
}

// recoveryProbe checks one channel's upstream.
func (s *Service) recoveryProbe(ctx context.Context, rt *Runtime) (probeOutcome, string) {
	if rt.Type == TypeCustom {
		return s.recoveryProbeCustom(ctx, rt)
	}
	status, body, _, err := s.probe(ctx, rt)
	return classifyProbe(status, body, err)
}

// classifyProbe maps a model-list probe to a health outcome. Only the
// failures the gateway counts against a channel's health (connection errors,
// timeouts, 401/403, 408, 429, 5xx) mark it unhealthy; other statuses (404 or
// 405 from upstreams without a model list endpoint, 400, …) are inconclusive.
func classifyProbe(status int, body []byte, err error) (probeOutcome, string) {
	switch {
	case err != nil:
		return probeUnhealthy, describeNetErr(err)
	case status == http.StatusOK:
		return probeHealthy, ""
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusRequestTimeout,
		status == http.StatusTooManyRequests, status >= 500:
		return probeUnhealthy, fmt.Sprintf("上游返回 %d：%s", status, upstreamMsg(body))
	default:
		return probeUnknown, ""
	}
}

func (s *Service) recoveryProbeCustom(ctx context.Context, rt *Runtime) (probeOutcome, string) {
	c := rt.Channel
	res, _, err := s.customCapability(ctx, &c, "health.check", "models.list")
	if err != nil || res == nil {
		return probeUnknown, ""
	}
	if res.Error != nil {
		return probeUnhealthy, "插件能力执行失败：" + *res.Error
	}
	var health struct {
		OK      *bool  `json:"ok"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(res.Output, &health)
	if health.OK != nil && !*health.OK {
		msg := "健康检查未通过"
		if health.Message != "" {
			msg += "：" + health.Message
		}
		return probeUnhealthy, msg
	}
	return probeHealthy, ""
}
