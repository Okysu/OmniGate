package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"
)

// Bytes is a Session.Call argument passed to JavaScript as a Uint8Array.
type Bytes []byte

type stateArg struct{}

// State is a Session.Call argument standing for the session's state object: a
// plain JavaScript object created with the session and kept in its runtime
// across calls (parseStream's state, phase9-api.md §2).
var State = stateArg{}

// Session pins one runtime for a multi-call exchange: a custom-protocol
// request (buildRequest → signRequest → parseResponse / parseStream… →
// endStream) runs every hook in the same runtime (ADR-0002 §2). The
// per-program concurrency slot is taken per call, not for the session's
// lifetime, so long streams don't starve other requests. Each call has its
// own timeout and all calls share the session's total time budget.
//
// A Session is not safe for concurrent use.
type Session struct {
	p      *Program
	ent    *vmEntry
	cs     *callState
	state  *goja.Object
	budget time.Duration
	used   time.Duration
	dead   bool // interrupted: the runtime is discarded on Close
	closed bool
}

// NewSession borrows a runtime for env. budget bounds the summed execution
// time of all calls (0 = unlimited).
func (p *Program) NewSession(ctx context.Context, env *Env, budget time.Duration) (*Session, error) {
	var ent *vmEntry
	select {
	case ent = <-p.pool:
	default:
		var err error
		if ent, err = p.newVM(); err != nil {
			return nil, err
		}
	}
	s := &Session{p: p, ent: ent, budget: budget}
	s.cs = newCallState(ctx, p.e, ent.vm, env)
	s.cs.bind()
	s.state = ent.vm.NewObject()
	return s, nil
}

// Used is the execution time consumed so far.
func (s *Session) Used() time.Duration { return s.used }

// Logs returns the log lines written by the plugin during the session.
func (s *Session) Logs() []LogLine {
	if s.cs.logs == nil {
		return []LogLine{}
	}
	return s.cs.logs
}

// Fetches returns the og.fetch calls made during the session.
func (s *Session) Fetches() []FetchRecord {
	if s.cs.fetches == nil {
		return []FetchRecord{}
	}
	return s.cs.fetches
}

// Call invokes the function at path. timeout bounds this call (further capped
// by the remaining budget); maxOutput bounds the JSON result (0 = engine
// default). Arguments are JSON-encodable values, Bytes or State.
func (s *Session) Call(ctx context.Context, path []string, timeout time.Duration, maxOutput int, args ...any) (json.RawMessage, error) {
	if s.closed || s.dead {
		return nil, &Error{Kind: "cancelled", Message: "插件运行时已中断"}
	}
	limit, byBudget := timeout, false
	if s.budget > 0 {
		remaining := s.budget - s.used
		if remaining <= 0 {
			return nil, s.budgetError()
		}
		if remaining < limit {
			limit, byBudget = remaining, true
		}
	}
	// Concurrency slot (CPU share), taken per call.
	wait := time.NewTimer(acquireWait)
	select {
	case s.p.sem <- struct{}{}:
		wait.Stop()
	case <-ctx.Done():
		wait.Stop()
		return nil, &Error{Kind: "cancelled", Message: "调用已取消"}
	case <-wait.C:
		return nil, &Error{Kind: "busy", Message: "插件并发执行已达上限"}
	}
	defer func() { <-s.p.sem }()

	vm := s.ent.vm
	jsArgs := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case Bytes:
			jsArgs[i] = newUint8Array(vm, v)
		case stateArg:
			jsArgs[i] = goja.Value(s.state)
		default:
			jsArgs[i] = a
		}
	}

	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	start := time.Now()
	x := &execution{vm: vm, prog: s.p, start: start}
	untrack := s.p.e.track(x)
	s.cs.ctx = cctx
	s.cs.maxOutput = s.p.e.cfg.MaxOutput
	if maxOutput > 0 {
		s.cs.maxOutput = maxOutput
	}
	fired := make(chan struct{})
	stop := context.AfterFunc(cctx, func() {
		defer close(fired)
		kind := "timeout"
		if !errors.Is(cctx.Err(), context.DeadlineExceeded) {
			kind = "cancelled"
		}
		vm.Interrupt(&interrupt{kind: kind})
	})
	out, err := s.cs.invoke(s.ent, path, jsArgs...)
	if !stop() {
		<-fired // the interrupt was delivered (possibly after the call returned)
	}
	vm.ClearInterrupt()
	untrack()
	s.used += time.Since(start)
	s.ent.uses++

	var perr *Error
	if errors.As(err, &perr) && (perr.Kind == "timeout" || perr.Kind == "memory" || perr.Kind == "cancelled") {
		s.dead = true
		if perr.Kind != "cancelled" && s.p.e.cfg.OnViolation != nil {
			s.p.e.cfg.OnViolation(s.p.key, perr.Kind)
		}
		if perr.Kind == "timeout" && byBudget {
			return nil, s.budgetError()
		}
		if perr.Kind == "timeout" {
			return nil, &Error{Kind: "timeout", Message: fmt.Sprintf("执行超时（单次调用上限 %s）", timeout)}
		}
	}
	return out, err
}

func (s *Session) budgetError() *Error {
	return &Error{Kind: "timeout", Message: fmt.Sprintf("本次请求的插件总执行时间超过 %s，已中断", s.budget)}
}

// SubstituteHeaders replaces secret handles created during the session in a
// returned request's header values.
func (s *Session) SubstituteHeaders(raw json.RawMessage) (json.RawMessage, error) {
	return s.cs.substituteHeaders(raw)
}

// Close returns the runtime to the pool (or discards it after an interrupt).
func (s *Session) Close() {
	if s.closed {
		return
	}
	s.closed = true
	vm := s.ent.vm
	vm.ClearInterrupt()
	_ = vm.Set("og", vm.NewObject())
	s.state = nil
	if s.dead {
		return
	}
	s.p.release(s.ent, false)
}
