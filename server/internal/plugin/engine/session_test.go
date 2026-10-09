package engine_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"omnigate/internal/plugin"
	"omnigate/internal/plugin/engine"
)

const sessionSrc = `
import { definePlugin } from "@omnigate/plugin-sdk"
export default definePlugin({
  parseStream(chunk: Uint8Array, state: any) {
    if (!state.dec) { state.dec = new TextDecoder(); state.n = 0 }
    state.n++
    const text = state.dec.decode(chunk, { stream: true })
    if (text === "spin") { const t = Date.now(); while (Date.now() - t < 30) {} }
    if (text === "loop") { while (true) {} }
    return [{ type: "delta", content: text, n: state.n, isBytes: chunk instanceof Uint8Array }]
  },
  endStream(state: any) { return [{ type: "finish", reason: "stop", calls: state.n }] },
  encode(s: string) { return Array.from(new TextEncoder().encode(s)) },
  pure() { return [typeof og.fetch, typeof og.storage, typeof og.secret, typeof og.log.info, og.crypto.sha256("a")] },
})`

func sessionProgram(t *testing.T, onViolation func(string, string)) *engine.Program {
	t.Helper()
	bundle, ds := plugin.Compile(map[string]string{"src/index.ts": sessionSrc}, "src/index.ts")
	if bundle == "" {
		t.Fatalf("compile: %+v", ds)
	}
	e := engine.New(engine.Config{OnViolation: onViolation}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p, err := e.Program("session", bundle)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSessionKeepsStateAndDecodesSplitUTF8(t *testing.T) {
	p := sessionProgram(t, nil)
	ctx := context.Background()
	s, err := p.NewSession(ctx, &engine.Env{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	word := []byte("你好")
	var got strings.Builder
	for i, chunk := range [][]byte{word[:2], word[2:4], word[4:]} {
		out, err := s.Call(ctx, []string{"parseStream"}, 50*time.Millisecond, 0, engine.Bytes(chunk), engine.State)
		if err != nil {
			t.Fatal(err)
		}
		var evs []struct {
			Content string `json:"content"`
			N       int    `json:"n"`
			IsBytes bool   `json:"isBytes"`
		}
		if err := json.Unmarshal(out, &evs); err != nil || len(evs) != 1 || evs[0].N != i+1 || !evs[0].IsBytes {
			t.Fatalf("call %d: %s %v", i, out, err)
		}
		got.WriteString(evs[0].Content)
	}
	if got.String() != "你好" {
		t.Fatalf("decoded %q", got.String())
	}
	out, err := s.Call(ctx, []string{"endStream"}, 50*time.Millisecond, 0, engine.State)
	if err != nil || !strings.Contains(string(out), `"calls":3`) {
		t.Fatalf("endStream = %s %v", out, err)
	}
	out, err = s.Call(ctx, []string{"encode"}, 50*time.Millisecond, 0, "é")
	if err != nil || string(out) != "[195,169]" {
		t.Fatalf("encode = %s %v", out, err)
	}
	if s.Used() <= 0 {
		t.Fatal("used time not tracked")
	}
}

func TestSessionPerCallTimeoutAndBudget(t *testing.T) {
	var violations atomic.Int32
	p := sessionProgram(t, func(string, string) { violations.Add(1) })
	ctx := context.Background()

	s, _ := p.NewSession(ctx, &engine.Env{}, 5*time.Second)
	_, err := s.Call(ctx, []string{"parseStream"}, 50*time.Millisecond, 0, engine.Bytes("loop"), engine.State)
	var pe *engine.Error
	if !asEngineErr(err, &pe) || pe.Kind != "timeout" || !strings.Contains(pe.Message, "单次调用") {
		t.Fatalf("loop = %v", err)
	}
	if _, err := s.Call(ctx, []string{"endStream"}, 50*time.Millisecond, 0, engine.State); err == nil {
		t.Fatal("an interrupted session must not run further calls")
	}
	s.Close()

	// Each call stays below the per-call limit, the sum exceeds the budget.
	s, _ = p.NewSession(ctx, &engine.Env{}, 100*time.Millisecond)
	defer s.Close()
	var last error
	calls := 0
	for ; calls < 20; calls++ {
		if _, last = s.Call(ctx, []string{"parseStream"}, 50*time.Millisecond, 0, engine.Bytes("spin"), engine.State); last != nil {
			break
		}
	}
	if !asEngineErr(last, &pe) || pe.Kind != "timeout" || !strings.Contains(pe.Message, "总执行时间") || calls < 2 || calls > 4 {
		t.Fatalf("budget: calls=%d err=%v", calls, last)
	}
	if violations.Load() != 2 {
		t.Fatalf("violations = %d", violations.Load())
	}
}

func TestPureEnvHidesHostAccess(t *testing.T) {
	p := sessionProgram(t, nil)
	r, err := p.Call(context.Background(), &engine.Env{Pure: true, Secrets: map[string]string{"apiKey": "x"}}, []string{"pure"}, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	want := `["undefined","undefined","undefined","function","ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"]`
	if string(r.Output) != want {
		t.Fatalf("pure = %s", r.Output)
	}
}

func asEngineErr(err error, pe **engine.Error) bool {
	e, ok := err.(*engine.Error)
	if ok {
		*pe = e
	}
	return ok
}
