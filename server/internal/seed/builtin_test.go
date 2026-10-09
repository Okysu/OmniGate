package seed

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuiltinCatalog checks the embedded catalog without a database: GPT
// prices and model information plus the six plans, in USD.
func TestBuiltinCatalog(t *testing.T) {
	c, err := Parse(bytes.NewReader(Builtin()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Currency != "USD" || len(c.Prices) != 8 || len(c.ModelInfo) != 8 || len(c.Plans) != 6 {
		t.Fatalf("builtin catalog: currency %q, %d prices, %d model info, %d plans", c.Currency, len(c.Prices), len(c.ModelInfo), len(c.Plans))
	}
	for _, p := range c.Prices {
		if !strings.HasPrefix(p.Model, "gpt-") || p.Kind != "sell" || p.ChannelName != nil {
			t.Errorf("unexpected price entry %+v", p)
		}
	}
	if Hash(Builtin()) != Hash(builtinCatalog) || len(Hash(nil)) != 64 {
		t.Fatal("hash")
	}
	// Builtin returns a copy: callers cannot alter the embedded catalog.
	b := Builtin()
	b[0] = 'X'
	if builtinCatalog[0] == 'X' {
		t.Fatal("Builtin must return a copy")
	}
}

type logBuffer struct{ bytes.Buffer }

func (l *logBuffer) logger() *slog.Logger { return slog.New(slog.NewTextHandler(&l.Buffer, nil)) }

func (f *fixture) onStart(spec string, logs *logBuffer) OnStartOutcome {
	f.t.Helper()
	if logs == nil {
		logs = &logBuffer{}
	}
	return OnStart(f.ctx, f.svc, spec, "test", logs.logger())
}

func (f *fixture) marker() *Marker {
	f.t.Helper()
	m, err := ReadMarker(f.ctx, f.svc.Pool)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func TestOnStartBuiltinOnce(t *testing.T) {
	f := newFixture(t)
	if got := f.onStart("", nil); got != OnStartDisabled {
		t.Fatalf("empty spec: %s", got)
	}
	if f.marker() != nil {
		t.Fatal("marker written while disabled")
	}

	// First start: everything is created and the marker recorded.
	var logs logBuffer
	if got := f.onStart("builtin", &logs); got != OnStartApplied {
		t.Fatalf("first start: %s\n%s", got, logs.String())
	}
	p, i, pl, a := f.catalogRows()
	if p != 8 || i != 8 || pl != 6 || a != 22 {
		t.Fatalf("rows prices=%d info=%d plans=%d audit=%d", p, i, pl, a)
	}
	m := f.marker()
	if m == nil || m.Source != SourceBuiltin || m.SHA256 != Hash(Builtin()) || m.Trigger != "startup" || m.Changes != 22 ||
		m.BinaryVersion != "test" || time.Since(m.AppliedAt) > time.Minute {
		t.Fatalf("marker %+v", m)
	}
	if !strings.Contains(logs.String(), "catalog seeded on first start") {
		t.Fatalf("log:\n%s", logs.String())
	}

	// Second start: skipped, nothing written.
	logs.Reset()
	if got := f.onStart("builtin", &logs); got != OnStartSkippedMarker {
		t.Fatalf("second start: %s", got)
	}
	if p2, i2, pl2, a2 := f.catalogRows(); p2 != p || i2 != i || pl2 != pl || a2 != a {
		t.Fatalf("second start wrote rows: %d %d %d %d", p2, i2, pl2, a2)
	}
	if !strings.Contains(logs.String(), "already seeded once") || strings.Contains(logs.String(), "has changed") {
		t.Fatalf("log:\n%s", logs.String())
	}

	// Manual `seed builtin` is idempotent.
	rep := f.apply(parse(t, string(Builtin())), false)
	if rep.Pending() != 0 || rep.Applied() != 0 || len(rep.Changes) != 22 {
		t.Fatalf("manual re-apply: pending %d applied %d changes %d", rep.Pending(), rep.Applied(), len(rep.Changes))
	}
	if p2, _, _, a2 := f.catalogRows(); p2 != p || a2 != a {
		t.Fatal("manual re-apply wrote rows")
	}

	// A newer built-in catalog (different hash) is not applied at start-up,
	// only reported with the manual command.
	m.SHA256 = strings.Repeat("0", 64)
	if err := WriteMarker(f.ctx, f.svc.Pool, *m); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if got := f.onStart("builtin", &logs); got != OnStartSkippedMarker {
		t.Fatalf("changed catalog: %s", got)
	}
	if !strings.Contains(logs.String(), "has changed") || !strings.Contains(logs.String(), "omnigate seed builtin --dry-run") {
		t.Fatalf("log:\n%s", logs.String())
	}
	if f.count(`SELECT version FROM system_settings WHERE key = 'seed.applied'`) != 2 {
		t.Fatal("marker upsert did not bump the version")
	}
}

// TestOnStartAlreadySeeded: a database seeded by hand before markers existed
// gets only the marker — no new price versions, plan updates or audit rows.
func TestOnStartAlreadySeeded(t *testing.T) {
	f := newFixture(t)
	f.apply(parse(t, string(Builtin())), false)
	p, i, pl, a := f.catalogRows()
	if f.marker() != nil {
		t.Fatal("Apply alone must not write the marker")
	}
	if got := f.onStart("builtin", nil); got != OnStartApplied {
		t.Fatalf("outcome %s", got)
	}
	if p2, i2, pl2, a2 := f.catalogRows(); p2 != p || i2 != i || pl2 != pl || a2 != a {
		t.Fatalf("rows changed: prices %d→%d info %d→%d plans %d→%d audit %d→%d", p, p2, i, i2, pl, pl2, a, a2)
	}
	if m := f.marker(); m == nil || m.Changes != 0 {
		t.Fatalf("marker %+v", m)
	}
}

func TestOnStartCurrencyMismatch(t *testing.T) {
	f := newFixture(t)
	f.exec(`UPDATE system_settings SET value = $1 WHERE key = 'billing.currency'`, []byte(`{"code":"CNY","symbol":"¥","decimals":2}`))
	var logs logBuffer
	if got := f.onStart("builtin", &logs); got != OnStartSkippedCurrency {
		t.Fatalf("outcome %s", got)
	}
	if p, i, pl, a := f.catalogRows(); p+i+pl+a != 0 {
		t.Fatalf("rows written: %d %d %d %d", p, i, pl, a)
	}
	if f.marker() != nil {
		t.Fatal("marker written on currency mismatch")
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "settlement_currency=CNY") {
		t.Fatalf("log:\n%s", logs.String())
	}
}

func TestOnStartFile(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"prices": [{"model": ""}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.onStart(filepath.Join(dir, "missing.json"), nil); got != OnStartFailed {
		t.Fatalf("missing file: %s", got)
	}
	if got := f.onStart(bad, nil); got != OnStartFailed {
		t.Fatalf("invalid file: %s", got)
	}
	if f.marker() != nil {
		t.Fatal("marker written after a failure")
	}
	good := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(good, []byte(catalogJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.onStart(good, nil); got != OnStartApplied {
		t.Fatalf("file: %s", got)
	}
	if m := f.marker(); m == nil || m.Source != "file:"+good || m.Changes != 7 {
		t.Fatalf("marker %+v", m)
	}
	// Any earlier seed run (here from a file) makes the start-up seed a no-op.
	if got := f.onStart("builtin", nil); got != OnStartSkippedMarker {
		t.Fatalf("builtin after file: %s", got)
	}
	if _, _, pl, _ := f.catalogRows(); pl != 2 {
		t.Fatalf("plans %d", pl)
	}
}

func TestOnStartCancelled(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if got := OnStart(ctx, f.svc, "builtin", "test", slog.New(slog.DiscardHandler)); got != OnStartFailed {
		t.Fatalf("outcome %s", got)
	}
}
