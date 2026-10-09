// Package engine runs plugin JavaScript in pooled Goja runtimes with timeouts,
// cancellation, a process-heap watchdog and a capability-scoped host API
// (ADR-0002). Plugins get no filesystem, process, timers or module loader.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// GlobalName must match plugin.GlobalName (the compiled IIFE global).
const GlobalName = "__og_plugin"

type Config struct {
	HeapLimit     uint64        // interrupt when process heap grows by this much during executions
	MaxConcurrent int           // concurrent executions per program
	MaxIdle       int           // pooled runtimes per program
	MaxOutput     int           // bytes of JSON output per call
	MaxFetches    int           // og.fetch calls per execution
	MaxFetchBody  int64         // bytes read per og.fetch response
	LoadTimeout   time.Duration // time allowed for top-level module code
	// OnViolation is called after a timeout or memory interrupt.
	OnViolation func(programKey, kind string)
}

func (c *Config) defaults() {
	if c.HeapLimit == 0 {
		c.HeapLimit = 256 << 20
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 8
	}
	if c.MaxIdle == 0 {
		c.MaxIdle = 8
	}
	if c.MaxOutput == 0 {
		c.MaxOutput = 256 << 10
	}
	if c.MaxFetches == 0 {
		c.MaxFetches = 10
	}
	if c.MaxFetchBody == 0 {
		c.MaxFetchBody = 4 << 20
	}
	if c.LoadTimeout == 0 {
		c.LoadTimeout = 2 * time.Second
	}
}

// Error is a classified plugin execution failure.
type Error struct {
	Kind    string `json:"kind"` // exception | timeout | memory | cancelled | busy | output | missing | promise | load
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

func (e *Error) Error() string { return "plugin " + e.Kind + ": " + e.Message }

type Engine struct {
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	programs map[string]*Program

	amu    sync.Mutex
	active map[*execution]struct{}
}

type execution struct {
	vm          *goja.Runtime
	prog        *Program
	start       time.Time
	heapAtStart uint64
	interrupted bool
}

func New(cfg Config, log *slog.Logger) *Engine {
	cfg.defaults()
	return &Engine{cfg: cfg, log: log, programs: map[string]*Program{}, active: map[*execution]struct{}{}}
}

// RunWatchdog samples the heap every 10ms while executions are active and
// interrupts the longest-running one when growth exceeds HeapLimit.
func (e *Engine) RunWatchdog(ctx context.Context) {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		e.amu.Lock()
		if len(e.active) == 0 {
			e.amu.Unlock()
			continue
		}
		metrics.Read(sample)
		heap := sample[0].Value.Uint64()
		var base uint64 = ^uint64(0)
		var oldest *execution
		for x := range e.active {
			base = min(base, x.heapAtStart)
			if !x.interrupted && (oldest == nil || x.start.Before(oldest.start)) {
				oldest = x
			}
		}
		if oldest != nil && heap > base && heap-base > e.cfg.HeapLimit {
			oldest.interrupted = true
			oldest.vm.Interrupt(&interrupt{kind: "memory"})
		}
		e.amu.Unlock()
	}
}

func heapNow() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

func (e *Engine) track(x *execution) func() {
	x.heapAtStart = heapNow()
	e.amu.Lock()
	e.active[x] = struct{}{}
	e.amu.Unlock()
	return func() {
		e.amu.Lock()
		delete(e.active, x)
		e.amu.Unlock()
	}
}

// Program is a compiled plugin bundle with its runtime pool.
type Program struct {
	key  string
	prog *goja.Program
	e    *Engine
	pool chan *vmEntry
	sem  chan struct{}
}

type vmEntry struct {
	vm        *goja.Runtime
	plugin    *goja.Object
	parse     goja.Callable
	stringify goja.Callable
	uses      int
}

// Program returns (and caches) the compiled program for key (a content hash).
func (e *Engine) Program(key, bundle string) (*Program, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.programs[key]; ok {
		return p, nil
	}
	prog, err := goja.Compile(key+".js", bundle, true)
	if err != nil {
		return nil, &Error{Kind: "load", Message: err.Error()}
	}
	p := &Program{key: key, prog: prog, e: e, pool: make(chan *vmEntry, e.cfg.MaxIdle), sem: make(chan struct{}, e.cfg.MaxConcurrent)}
	// Validate by instantiating once.
	ent, err := p.newVM()
	if err != nil {
		return nil, err
	}
	p.release(ent, false)
	e.programs[key] = p
	return p, nil
}

// Drop forgets a cached program (used for short-lived draft builds).
func (e *Engine) Drop(key string) {
	e.mu.Lock()
	delete(e.programs, key)
	e.mu.Unlock()
}

func (p *Program) newVM() (*vmEntry, error) {
	vm := goja.New()
	vm.SetMaxCallStackSize(512)
	timer := time.AfterFunc(p.e.cfg.LoadTimeout, func() { vm.Interrupt(&interrupt{kind: "timeout"}) })
	defer timer.Stop()
	// Placeholder host object so top-level references don't crash; replaced per call.
	_ = vm.Set("og", vm.NewObject())
	installTextCodec(vm)
	if _, err := vm.RunProgram(p.prog); err != nil {
		return nil, &Error{Kind: "load", Message: jsError(err)}
	}
	root := vm.Get(GlobalName)
	if root == nil || goja.IsUndefined(root) {
		return nil, &Error{Kind: "load", Message: "插件模块没有导出"}
	}
	def := root.ToObject(vm).Get("default")
	if def == nil || goja.IsUndefined(def) || goja.IsNull(def) {
		return nil, &Error{Kind: "load", Message: "插件必须 export default definePlugin({...})"}
	}
	jsonObj := vm.Get("JSON").ToObject(vm)
	parse, _ := goja.AssertFunction(jsonObj.Get("parse"))
	stringify, _ := goja.AssertFunction(jsonObj.Get("stringify"))
	return &vmEntry{vm: vm, plugin: def.ToObject(vm), parse: parse, stringify: stringify}, nil
}

func (p *Program) acquire(ctx context.Context) (*vmEntry, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, &Error{Kind: "busy", Message: "插件并发执行已达上限"}
	}
	select {
	case ent := <-p.pool:
		return ent, nil
	default:
	}
	ent, err := p.newVM()
	if err != nil {
		<-p.sem
		return nil, err
	}
	return ent, nil
}

func (p *Program) release(ent *vmEntry, held bool) {
	if held {
		<-p.sem
	}
	if ent == nil || ent.uses >= 500 {
		return
	}
	select {
	case p.pool <- ent:
	default:
	}
}

type interrupt struct{ kind string }

func lookup(vm *goja.Runtime, root *goja.Object, path []string) (goja.Callable, goja.Value) {
	var this goja.Value = root
	cur := goja.Value(root)
	for _, seg := range path {
		if cur == nil || goja.IsUndefined(cur) || goja.IsNull(cur) {
			return nil, nil
		}
		obj := cur.ToObject(vm)
		this = obj
		cur = obj.Get(seg)
	}
	if cur == nil {
		return nil, nil
	}
	fn, ok := goja.AssertFunction(cur)
	if !ok {
		return nil, nil
	}
	return fn, this
}

// Has reports whether the plugin exports a function at path.
func (p *Program) Has(path ...string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ent, err := p.acquire(ctx)
	if err != nil {
		return false
	}
	defer p.release(ent, true)
	fn, _ := lookup(ent.vm, ent.plugin, path)
	return fn != nil
}

// Result of a call.
type Result struct {
	Output     json.RawMessage `json:"output"`
	Logs       []LogLine       `json:"logs"`
	Fetches    []FetchRecord   `json:"fetches"`
	DurationMs int64           `json:"durationMs"`
}

// Call invokes the function at path with JSON-encodable args.
func (p *Program) Call(ctx context.Context, env *Env, path []string, timeout time.Duration, args ...any) (*Result, error) {
	return p.run(ctx, env, timeout, func(ent *vmEntry, cs *callState) (json.RawMessage, error) {
		return cs.invoke(ent, path, args...)
	})
}

// RunHooks applies transformRequest then signRequest (those present in hooks)
// to req in one runtime. Secret handles in returned header values are replaced
// with plaintext before returning.
func (p *Program) RunHooks(ctx context.Context, env *Env, hooks []string, req any, timeoutEach time.Duration) (*Result, error) {
	in, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	// Each hook copies the whole request in and out of the runtime, so its
	// budget grows with the body size: a large (legitimate) request must not
	// time out — and count as a plugin violation — just because the host is
	// busy (security audit: users could otherwise get plugins auto-disabled).
	each := HookBudget(timeoutEach, len(in))
	return p.run(ctx, env, each*time.Duration(max(len(hooks), 1)), func(ent *vmEntry, cs *callState) (json.RawMessage, error) {
		cur := in
		// Hooks return the whole request, so the output budget scales with the
		// input (the data plane accepts up to 32 MiB bodies) instead of the
		// capability output limit.
		cs.maxOutput = min(2*len(cur)+(1<<20), 80<<20)
		for _, h := range hooks {
			out, err := cs.invoke(ent, []string{h}, json.RawMessage(cur), env.Context)
			if err != nil {
				return nil, err
			}
			cur = out
		}
		return cs.substituteHeaders(cur)
	})
}

// HookBudget is the time one hook may take for an input of n bytes: base plus
// 20ms per started 64 KiB, at most hookBudgetMax.
func HookBudget(base time.Duration, n int) time.Duration {
	return min(base+time.Duration((n+64<<10-1)/(64<<10))*20*time.Millisecond, max(base, hookBudgetMax))
}

const hookBudgetMax = 2 * time.Second

// acquireWait bounds how long a call waits for a free execution slot; the
// call's own timeout only starts once it has a runtime.
const acquireWait = time.Second

func (p *Program) run(ctx context.Context, env *Env, timeout time.Duration, fn func(*vmEntry, *callState) (json.RawMessage, error)) (*Result, error) {
	start := time.Now()
	actx, acancel := context.WithTimeout(ctx, timeout+acquireWait)
	ent, err := p.acquire(actx)
	acancel()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	x := &execution{vm: ent.vm, prog: p, start: start}
	untrack := p.e.track(x)
	cs := newCallState(ctx, p.e, ent.vm, env)
	cs.bind()
	stop := context.AfterFunc(ctx, func() {
		kind := "timeout"
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = "cancelled"
		}
		ent.vm.Interrupt(&interrupt{kind: kind})
	})
	out, err := fn(ent, cs)
	stop()
	untrack()
	ent.uses++
	interrupted := false
	var perr *Error
	if errors.As(err, &perr) && (perr.Kind == "timeout" || perr.Kind == "memory" || perr.Kind == "cancelled") {
		interrupted = true
		if perr.Kind != "cancelled" && p.e.cfg.OnViolation != nil {
			p.e.cfg.OnViolation(p.key, perr.Kind)
		}
	}
	ent.vm.ClearInterrupt()
	_ = ent.vm.Set("og", ent.vm.NewObject())
	if interrupted {
		p.release(nil, true) // discard a runtime that was interrupted mid-flight
	} else {
		p.release(ent, true)
	}
	res := &Result{Output: out, Logs: cs.logs, Fetches: cs.fetches, DurationMs: time.Since(start).Milliseconds()}
	if res.Logs == nil {
		res.Logs = []LogLine{}
	}
	if res.Fetches == nil {
		res.Fetches = []FetchRecord{}
	}
	return res, err
}

func classify(vm *goja.Runtime, err error) *Error {
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		if in, ok := ie.Value().(*interrupt); ok {
			msg := map[string]string{"timeout": "执行超时", "memory": "内存占用超过上限，已中断", "cancelled": "调用已取消"}[in.kind]
			return &Error{Kind: in.kind, Message: msg}
		}
		return &Error{Kind: "cancelled", Message: fmt.Sprint(ie.Value())}
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return &Error{Kind: "exception", Message: jsError(err), Stack: truncate(ex.String(), 4000)}
	}
	var se *goja.StackOverflowError
	if errors.As(err, &se) {
		return &Error{Kind: "exception", Message: "调用栈溢出（递归过深）"}
	}
	return &Error{Kind: "exception", Message: err.Error()}
}

func jsError(err error) string {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		if v := ex.Value(); v != nil {
			if o, ok := v.(*goja.Object); ok {
				if m := o.Get("message"); m != nil && !goja.IsUndefined(m) {
					name := "Error"
					if n := o.Get("name"); n != nil && !goja.IsUndefined(n) {
						name = n.String()
					}
					return name + ": " + m.String()
				}
			}
			return v.String()
		}
	}
	return err.Error()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
