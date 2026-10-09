package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
	"omnigate/internal/seed"
)

const cliCatalog = `{
  "prices": [{"kind": "sell", "model": "m1", "inputPerM": "1", "outputPerM": "2"}],
  "modelInfo": [{"model": "m1", "displayName": "M1"}],
  "plans": [{"name": "Go", "listPrice": "3", "duration": "30d", "rules": [
    {"id": "weekly", "label": "每周滚动限额", "meter": "charge", "window": {"kind": "rolling", "duration": "7d"}, "limit": "12"}]}]
}`

func TestSeedCommand(t *testing.T) {
	u := dbtest.URL(t)
	pool := dbtest.OpenURL(t, u) // migrated
	t.Setenv("OMNIGATE_DATABASE_URL", u)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(file, []byte(cliCatalog), 0o600); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM prices) + (SELECT count(*) FROM model_info) + (SELECT count(*) FROM plans)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	run := func(stdin string, args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := seedCmd(args, strings.NewReader(stdin), &out, &errOut)
		return out.String() + errOut.String(), err
	}

	out, err := run("", file, "--dry-run")
	if err != nil || !strings.Contains(out, "试运行：未写入任何数据（3 项将会变更）") || count() != 0 {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	out, err = run(cliCatalog, "-") // catalog on stdin
	if err != nil || !strings.Contains(out, "已写入 3 项变更") || count() != 3 {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	out, err = run("", "--dry-run", file)
	if err != nil || !strings.Contains(out, "试运行：未写入任何数据（0 项将会变更）") {
		t.Fatalf("dry run after apply: %v\n%s", err, out)
	}

	// Validation errors exit non-zero with field paths and write nothing.
	bad := strings.Replace(cliCatalog, `"limit": "12"`, `"limit": "-1"`, 1)
	if _, err = run(bad, "-"); err == nil || !strings.Contains(err.Error(), "plans[0].rules[0].limit: ") {
		t.Fatalf("invalid: %v", err)
	}
	if _, err = run("", "a.json", "b.json"); err == nil {
		t.Fatal("two files must fail")
	}

	// Export: same format, applies as a no-op.
	out, err = run("", "export", "--prices", "--plans")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	exported, err := seed.Parse(strings.NewReader(out[:strings.LastIndex(out, "}")+1]))
	if err != nil {
		t.Fatalf("export does not parse: %v\n%s", err, out)
	}
	if len(exported.Prices) != 1 || len(exported.Plans) != 1 || exported.ModelInfo != nil {
		t.Fatalf("export = %+v", exported)
	}
}

func TestReadOnlyURL(t *testing.T) {
	u := dbtest.URL(t)
	dbtest.OpenURL(t, u)
	ro, err := readOnlyURL(u)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM plans`).Scan(&n); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM plans`); err == nil {
		t.Fatal("write through a read-only connection succeeded")
	}
	if db.IsSQLiteURL(u) {
		if _, err := readOnlyURL("sqlite://" + filepath.Join(t.TempDir(), "missing.db")); err == nil {
			t.Fatal("missing sqlite file must fail (opening would create it)")
		}
	}
}

func TestSeedBuiltinCommand(t *testing.T) {
	// export-builtin needs no database or configuration.
	var out bytes.Buffer
	if err := seedCmd([]string{"export-builtin"}, nil, &out, &out); err != nil || !bytes.Equal(out.Bytes(), seed.Builtin()) {
		t.Fatalf("export-builtin: %v", err)
	}

	u := dbtest.URL(t)
	pool := dbtest.OpenURL(t, u)
	t.Setenv("OMNIGATE_DATABASE_URL", u)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ('billing.currency', $1)`,
		[]byte(`{"code":"USD","symbol":"$","decimals":2}`)); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := seedCmd(args, strings.NewReader(""), &out, &out); err != nil {
			t.Fatalf("seed %v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	marker := func() *seed.Marker {
		t.Helper()
		m, err := seed.ReadMarker(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if o := run("builtin", "--dry-run"); !strings.Contains(o, "内置目录") || !strings.Contains(o, "（22 项将会变更）") || marker() != nil {
		t.Fatalf("dry run:\n%s", o)
	}
	if o := run("builtin"); !strings.Contains(o, "已写入 22 项变更") {
		t.Fatalf("apply:\n%s", o)
	}
	if m := marker(); m == nil || m.Source != seed.SourceBuiltin || m.Trigger != "command" || m.Changes != 22 || m.SHA256 != seed.Hash(seed.Builtin()) {
		t.Fatalf("marker %+v", m)
	}
	if o := run("builtin"); !strings.Contains(o, "无需变更") {
		t.Fatalf("second apply:\n%s", o)
	}
	if m := marker(); m == nil || m.Changes != 0 {
		t.Fatalf("marker after no-op %+v", m)
	}
}
