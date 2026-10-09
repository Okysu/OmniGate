package app_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"omnigate/internal/app"
	"omnigate/internal/config"
	"omnigate/internal/platform/db"
)

// TestSeedOnStart runs OMNIGATE_SEED_ON_START=builtin through app.New the way
// `omnigate serve` does: on a fresh database the currency is initialised
// first and the built-in catalog applied; later starts skip it. A deployment
// whose currency differs starts normally without seeding.
func TestSeedOnStart(t *testing.T) {
	ctx := context.Background()
	start := func(dsn string, env map[string]string) string {
		t.Helper()
		pool, err := db.Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
			t.Fatal(err)
		}
		all := map[string]string{"OMNIGATE_DATABASE_URL": dsn, "OMNIGATE_DATA_DIR": t.TempDir(), "OMNIGATE_SEED_ON_START": "builtin"}
		for k, v := range env {
			all[k] = v
		}
		cfg, err := config.LoadFrom(func(k string) string { return all[k] })
		if err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		if _, err := app.New(ctx, cfg, slog.New(slog.NewTextHandler(&logs, nil)), pool, app.Options{}); err != nil {
			t.Fatal(err)
		}
		return logs.String()
	}
	count := func(dsn, sql string) int {
		t.Helper()
		pool, err := db.Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		var n int
		if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	dsn := testDB(t)
	logs := start(dsn, nil)
	if !strings.Contains(logs, "catalog seeded on first start") {
		t.Fatalf("first start log:\n%s", logs)
	}
	if p, pl, m := count(dsn, `SELECT count(*) FROM prices`), count(dsn, `SELECT count(*) FROM plans`),
		count(dsn, `SELECT count(*) FROM system_settings WHERE key = 'seed.applied'`); p != 8 || pl != 6 || m != 1 {
		t.Fatalf("after first start: prices %d plans %d marker %d", p, pl, m)
	}
	audits := count(dsn, `SELECT count(*) FROM audit_logs`)

	logs = start(dsn, nil)
	if !strings.Contains(logs, "catalog seeding skipped") {
		t.Fatalf("second start log:\n%s", logs)
	}
	if count(dsn, `SELECT count(*) FROM prices`) != 8 || count(dsn, `SELECT count(*) FROM audit_logs`) != audits {
		t.Fatal("second start wrote catalog rows")
	}

	// Currency mismatch on first start: logged, not fatal, nothing seeded.
	cny := testDB(t)
	logs = start(cny, map[string]string{"OMNIGATE_CURRENCY": "CNY"})
	if !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "catalog currency differs") {
		t.Fatalf("CNY start log:\n%s", logs)
	}
	if count(cny, `SELECT count(*) FROM plans`) != 0 || count(cny, `SELECT count(*) FROM system_settings WHERE key = 'seed.applied'`) != 0 {
		t.Fatal("seeded despite the currency mismatch")
	}
}
