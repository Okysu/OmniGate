package transfer

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
)

// targetURL returns a fresh target database of the configured test backend.
func targetURL(t *testing.T) string {
	if havePostgres() {
		return pgURL(t)
	}
	return sqliteURL(t)
}

func migrated(t *testing.T, u string) *db.DB {
	t.Helper()
	return dbtest.OpenURL(t, u)
}

func addUser(t *testing.T, d *db.DB, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	mustExec(t, d, `INSERT INTO users (id, display_name) VALUES ($1, $2)`, id, name)
	return id
}

func count(t *testing.T, d *db.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := d.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func run(t *testing.T, o Options) (string, error) {
	t.Helper()
	var out bytes.Buffer
	o.Out = &out
	_, err := Run(context.Background(), o)
	return out.String(), err
}

func TestRunRefusesNonEmptyTarget(t *testing.T) {
	from, to := sqliteURL(t), targetURL(t)
	src := migrated(t, from)
	addUser(t, src, "from source")
	dst := migrated(t, to)
	stale := addUser(t, dst, "already here")

	out, err := run(t, Options{From: from, To: to})
	if err == nil || !strings.Contains(err.Error(), "target database is not empty: users (1 rows)") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if n := count(t, dst, `SELECT count(*) FROM users WHERE id = $1`, stale); n != 1 {
		t.Fatal("refused run changed the target")
	}
	// The dry run reports the same problem.
	if out, err := run(t, Options{From: from, To: to, DryRun: true}); err == nil || !strings.Contains(out, "dry run: no changes made") {
		t.Fatalf("dry run err = %v\n%s", err, out)
	}

	// --force-empty-check=false replaces the target's rows.
	out, err = run(t, Options{From: from, To: to, SkipEmptyCheck: true})
	if err != nil || !strings.Contains(out, "WARNING: the target is not empty") {
		t.Fatalf("forced run: %v\n%s", err, out)
	}
	if count(t, dst, `SELECT count(*) FROM users WHERE id = $1`, stale) != 0 || count(t, dst, `SELECT count(*) FROM users`) != 1 {
		t.Fatal("target rows not replaced")
	}
	// Migration seed rows (billing.enforce) came across from the source.
	if count(t, dst, `SELECT count(*) FROM system_settings WHERE key = 'billing.enforce'`) != 1 {
		t.Fatal("seed setting missing")
	}
	sameData(t, from, to)
}

func TestRunRefusesNewerSchema(t *testing.T) {
	latest, err := latestVersion(db.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	for _, side := range []string{"source", "target"} {
		t.Run(side, func(t *testing.T) {
			from, to := sqliteURL(t), targetURL(t)
			src, dst := migrated(t, from), migrated(t, to)
			ahead := src
			if side == "target" {
				ahead = dst
			}
			mustExec(t, ahead, `INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, latest+100)
			out, err := run(t, Options{From: from, To: to})
			if err == nil || !strings.Contains(err.Error(), side+" schema version") || !strings.Contains(err.Error(), "newer than this omnigate binary") {
				t.Fatalf("err = %v\n%s", err, out)
			}
		})
	}
}

func TestRunDryRunChangesNothing(t *testing.T) {
	from, to := sqliteURL(t), targetURL(t)
	ctx := context.Background()
	src := migrated(t, from)
	addUser(t, src, "u1")
	addUser(t, src, "u2")
	if err := db.MigrateDownTo(ctx, src, 9); err != nil {
		t.Fatal(err)
	}
	src.Close() // checkpoint the WAL so the file holds everything
	srcFile := strings.TrimPrefix(from, "sqlite://")
	before, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, Options{From: from, To: to, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{"would migrate source from version 9", "would migrate target from version 0", "dry run: no changes made",
		"target: empty", "notification_events", "(created by migration)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "users") || !strings.Contains(out, "         2 rows") {
		t.Errorf("source counts missing:\n%s", out)
	}
	// Nothing changed: the source is still at version 9, the target was not created/migrated.
	se, _ := parseEndpoint(from)
	if v, err := inspectVersion(ctx, se); err != nil || v != 9 {
		t.Fatalf("source version = %d %v", v, err)
	}
	if after, _ := os.ReadFile(srcFile); !bytes.Equal(before, after) {
		t.Error("source file changed")
	}
	if db.IsSQLiteURL(to) {
		if _, err := os.Stat(strings.TrimPrefix(to, "sqlite://")); !os.IsNotExist(err) {
			t.Fatalf("dry run created the target file: %v", err)
		}
	} else {
		dst := openRaw(t, to)
		if n := count(t, dst, `SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()`); n != 0 {
			t.Fatalf("dry run created %d tables in the target", n)
		}
	}

	// The real run migrates both and copies.
	out, err = run(t, Options{From: from, To: to, Batch: 1})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "migrating source from version 9") || !strings.Contains(out, "verified:") {
		t.Fatalf("output:\n%s", out)
	}
	sameData(t, from, to)
}

func TestRunArguments(t *testing.T) {
	requireDB(t)
	f := sqliteURL(t)
	for _, tc := range []struct {
		o    Options
		want string
	}{
		{Options{From: f, To: f}, "same database"},
		{Options{From: "sqlite:///nonexistent/x.db", To: f}, "does not exist"},
		{Options{From: "sqlite::memory:", To: f}, "must name a database file"},
		{Options{From: f, To: ""}, "--to"},
	} {
		_, err := Run(context.Background(), Options{From: tc.o.From, To: tc.o.To, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.o, err, tc.want)
		}
	}
}
