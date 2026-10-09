package transfer

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/platform/db"
)

// The synthetic schema covers every supported type, a foreign-key cycle
// through a nullable NOT DEFERRABLE column (parent.best_child ⇄
// child.parent_id), a self-reference (child.up), a cycle of NOT NULL
// DEFERRABLE constraints (d1 ⇄ d2), a serial column and money columns.
var syntheticPG = []string{
	`CREATE TABLE parent (id uuid PRIMARY KEY, name text NOT NULL, flag boolean NOT NULL, n integer, small smallint,
		big bigint NOT NULL DEFAULT 0, f double precision, at timestamptz NOT NULL, day date, doc jsonb NOT NULL,
		amount numeric(38, 9) NOT NULL DEFAULT 0, raw bytea NOT NULL, tags text[], nums bigint[], cost_nano bigint NOT NULL DEFAULT 0,
		seq bigserial, best_child uuid)`,
	`CREATE TABLE child (id uuid PRIMARY KEY, parent_id uuid NOT NULL REFERENCES parent (id), up uuid REFERENCES child (id),
		charge_nano bigint NOT NULL, note varchar(20))`,
	`ALTER TABLE parent ADD CONSTRAINT parent_best_child_fkey FOREIGN KEY (best_child) REFERENCES child (id)`,
	`CREATE TABLE d1 (id integer PRIMARY KEY, other integer NOT NULL)`,
	`CREATE TABLE d2 (id integer PRIMARY KEY, d1 integer NOT NULL REFERENCES d1 (id) DEFERRABLE)`,
	`ALTER TABLE d1 ADD FOREIGN KEY (other) REFERENCES d2 (id) DEFERRABLE INITIALLY IMMEDIATE`,
	`CREATE TABLE logs (id uuid NOT NULL, started_at timestamptz NOT NULL, charge_nano bigint NOT NULL DEFAULT 0,
		PRIMARY KEY (id, started_at)) PARTITION BY RANGE (started_at)`,
	`CREATE TABLE logs_default PARTITION OF logs DEFAULT`,
}

var syntheticSQLite = []string{
	`CREATE TABLE parent (id TEXT PRIMARY KEY, name TEXT NOT NULL, flag INTEGER NOT NULL, n INTEGER, small INTEGER,
		big INTEGER NOT NULL DEFAULT 0, f REAL, at TEXT NOT NULL, day TEXT, doc TEXT NOT NULL CHECK (json_valid(doc)),
		amount TEXT NOT NULL DEFAULT '0', raw BLOB NOT NULL, tags TEXT, nums TEXT, cost_nano INTEGER NOT NULL DEFAULT 0,
		seq INTEGER, best_child TEXT REFERENCES child (id)) STRICT`,
	`CREATE TABLE child (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES parent (id), up TEXT REFERENCES child (id),
		charge_nano INTEGER NOT NULL, note TEXT) STRICT`,
	`CREATE TABLE d1 (id INTEGER PRIMARY KEY, other INTEGER NOT NULL REFERENCES d2 (id)) STRICT`,
	`CREATE TABLE d2 (id INTEGER PRIMARY KEY, d1 INTEGER NOT NULL REFERENCES d1 (id)) STRICT`,
	`CREATE TABLE logs (id TEXT NOT NULL, started_at TEXT NOT NULL, charge_nano INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (id, started_at)) STRICT`,
}

func createSynthetic(t *testing.T, d *db.DB) {
	t.Helper()
	stmts := syntheticSQLite
	if d.Dialect() == db.Postgres {
		stmts = syntheticPG
	}
	for _, s := range stmts {
		mustExec(t, d, s)
	}
}

// fillSynthetic inserts rows exercising NULLs, edge values and the cycles.
func fillSynthetic(t *testing.T, d *db.DB) {
	t.Helper()
	ctx := context.Background()
	p1, p2 := uuid.MustParse("00000000-0000-4000-8000-000000000001"), uuid.MustParse("00000000-0000-4000-8000-000000000002")
	c1, c2, c3 := uuid.New(), uuid.New(), uuid.New()
	at := time.Date(2026, 9, 30, 23, 59, 59, 123456789, time.UTC)
	insParent := `INSERT INTO parent (id, name, flag, n, small, big, f, at, day, doc, amount, raw, tags, nums, cost_nano, seq)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`
	if d.Dialect() == db.Postgres {
		insParent = `INSERT INTO parent (id, name, flag, n, small, big, f, at, day, doc, amount, raw, tags, nums, cost_nano, seq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10::jsonb, $11::numeric, $12, $13::text::text[], $14::text::bigint[], $15, $16)`
	}
	arr := func(pg, lite string) any {
		if d.Dialect() == db.Postgres {
			return pg
		}
		return lite
	}
	day := "2026-02-28"
	if _, err := d.Exec(ctx, insParent, p1, "名字 \"quoted\" 'single'", true, int64(-2147483648), int64(32767), int64(9223372036854775807),
		3.25, at, day, `{"b": [1, 2.50, {"c": null}], "a": "xé"}`, "12.500000000", []byte{0, 1, 2, 255},
		arr(`{"a","b c",NULL}`, `["a","b c",null]`), arr(`{1,-2}`, `[1,-2]`), int64(1_500_000_000), int64(7)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, insParent, p2, "", false, nil, nil, int64(0), nil, at.Add(-40*24*time.Hour), nil, `[]`, "-0.000000001",
		[]byte{9}, nil, nil, int64(-25), int64(42)); err != nil {
		t.Fatal(err)
	}
	// An empty (not NULL) blob; the db layer would bind an empty []byte as NULL on SQLite.
	mustExec(t, d, `UPDATE parent SET raw = `+arr(`''::bytea`, `x''`).(string)+` WHERE id = $1`, p2)
	// child rows: c2 references c1 (self), c3 references c2; parent p1 points
	// at c3 (cycle).
	for _, c := range []struct {
		id, parent uuid.UUID
		up         any
		charge     int64
	}{{c1, p1, nil, 1}, {c2, p1, c1, 20}, {c3, p2, c2, 300}} {
		if _, err := d.Exec(ctx, `INSERT INTO child (id, parent_id, up, charge_nano, note) VALUES ($1, $2, $3, $4, 'n')`, c.id, c.parent, c.up, c.charge); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, d, `UPDATE parent SET best_child = $1 WHERE id = $2`, c3, p1)
	if d.Dialect() == db.Postgres {
		tx, err := d.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{`SET CONSTRAINTS ALL DEFERRED`, `INSERT INTO d1 VALUES (1, 10)`, `INSERT INTO d2 VALUES (10, 1)`} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	} else {
		tx, err := d.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{`PRAGMA defer_foreign_keys = ON`, `INSERT INTO d1 VALUES (1, 10)`, `INSERT INTO d2 VALUES (10, 1)`} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i, ts := range []time.Time{at, at.Add(time.Hour), at.AddDate(0, -2, 0)} {
		mustExec(t, d, `INSERT INTO logs (id, started_at, charge_nano) VALUES ($1, $2, $3)`, uuid.New(), ts, int64(i*1000+7))
	}
}

func runCopy(t *testing.T, from, to string) (*Result, string, error) {
	t.Helper()
	ctx := context.Background()
	se, _ := parseEndpoint(from)
	de, _ := parseEndpoint(to)
	r, err := openReader(ctx, se)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w, err := openWriter(ctx, de)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var out bytes.Buffer
	c := &copier{src: r, dst: w, batch: 2, out: &out}
	res, err := c.run(ctx)
	return res, out.String(), err
}

func TestCopySyntheticSchema(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from, to db.Dialect
	}{
		{"sqlite_to_sqlite", db.SQLite, db.SQLite},
		{"sqlite_to_postgres", db.SQLite, db.Postgres},
		{"postgres_to_sqlite", db.Postgres, db.SQLite},
		{"postgres_to_postgres", db.Postgres, db.Postgres},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to := urlFor(t, tc.from), urlFor(t, tc.to)
			src, dst := openRaw(t, from), openRaw(t, to)
			createSynthetic(t, src)
			createSynthetic(t, dst)
			fillSynthetic(t, src)
			res, out, err := runCopy(t, from, to)
			if err != nil {
				t.Fatalf("copy: %v\n%s", err, out)
			}
			if res.Rows != 2+3+1+1+3 {
				t.Fatalf("rows = %d\n%s", res.Rows, out)
			}
			for _, tr := range res.Tables {
				if tr.Name == "child" && tr.Sums["charge_nano"] != "321" {
					t.Fatalf("child sums = %v", tr.Sums)
				}
				if tr.Name == "parent" && tr.Sums["cost_nano"] != "1499999975" {
					t.Fatalf("parent sums = %v", tr.Sums)
				}
			}
			if tc.to == db.Postgres && !strings.Contains(out, "restoring") {
				t.Errorf("expected deferred foreign keys on PostgreSQL:\n%s", out)
			}
			sameData(t, from, to)

			if tc.to == db.Postgres {
				// The serial sequence continues after the copied values.
				var seq int64
				if err := dst.QueryRow(context.Background(), `SELECT nextval(pg_get_serial_sequence('parent', 'seq'))`).Scan(&seq); err != nil || seq != 43 {
					t.Fatalf("sequence = %d %v", seq, err)
				}
			}
			// Copying again replaces the rows (the empty check lives in Run).
			if _, out, err := runCopy(t, from, to); err != nil {
				t.Fatalf("second copy: %v\n%s", err, out)
			}
			sameData(t, from, to)
		})
	}
}

// TestCopyDetectsMismatch: a target trigger that changes money values makes
// verification fail (and the error names the column).
func TestCopyDetectsMismatch(t *testing.T) {
	from, to := sqliteURL(t), sqliteURL(t)
	src, dst := openRaw(t, from), openRaw(t, to)
	createSynthetic(t, src)
	createSynthetic(t, dst)
	fillSynthetic(t, src)
	mustExec(t, dst, `CREATE TRIGGER bump AFTER INSERT ON child BEGIN UPDATE child SET charge_nano = charge_nano + 1 WHERE id = NEW.id; END`)
	_, out, err := runCopy(t, from, to)
	if err == nil || !strings.Contains(err.Error(), "verification failed") || !strings.Contains(err.Error(), "child.charge_nano: sum 321 in the source, 324 in the target") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// TestCopyForeignKeyCheck: dangling references in the source are caught on a
// SQLite target by PRAGMA foreign_key_check and nothing is committed.
func TestCopyForeignKeyCheck(t *testing.T) {
	from, to := sqliteURL(t), sqliteURL(t)
	src, dst := openRaw(t, from), openRaw(t, to)
	createSynthetic(t, src)
	createSynthetic(t, dst)
	// Write a dangling row on a connection with foreign keys off.
	w, err := openWriter(context.Background(), endpoint{dialect: db.SQLite, path: strings.TrimPrefix(from, "sqlite://")})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Exec(context.Background(), `INSERT INTO child (id, parent_id, charge_nano) VALUES ('c', 'missing', 1)`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	_, out, err := runCopy(t, from, to)
	if err == nil || !strings.Contains(err.Error(), "foreign key check failed") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	var n int64
	if err := dst.QueryRow(context.Background(), `SELECT count(*) FROM child`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("target rows after failed copy = %d %v", n, err)
	}
}

func TestOrderTables(t *testing.T) {
	mk := func(name string, pk []string, cols map[string]bool, fks ...foreignKey) *table {
		tb := &table{Name: name, PK: pk, FKs: fks}
		for c, notNull := range cols {
			tb.Columns = append(tb.Columns, column{Name: c, NotNull: notNull})
		}
		return tb
	}
	fk := func(col, ref string, deferrable bool) foreignKey {
		return foreignKey{Name: col, Columns: []string{col}, RefTable: ref, RefColumns: []string{"id"}, Deferrable: deferrable}
	}
	users := mk("users", []string{"id"}, map[string]bool{"id": true})
	keys := mk("keys", []string{"id"}, map[string]bool{"id": true, "user_id": true, "rotated_from": false},
		fk("user_id", "users", false), fk("rotated_from", "keys", false))
	logs := mk("logs", nil, map[string]bool{"key_id": false}, fk("key_id", "keys", false))
	p, err := orderTables([]*table{logs, keys, users}, true)
	if err != nil {
		t.Fatal(err)
	}
	if names(p.Tables) != "users,keys,logs" || len(p.Deferred["keys"]) != 1 {
		t.Fatalf("order = %s deferred = %v", names(p.Tables), p.Deferred)
	}
	// Self-reference through a NOT NULL column cannot be copied with enforced keys…
	bad := mk("tree", []string{"id"}, map[string]bool{"id": true, "parent": true}, fk("parent", "tree", false))
	if _, err := orderTables([]*table{bad}, true); err == nil {
		t.Fatal("expected an error for a NOT NULL self-reference")
	}
	// …unless the constraint is deferrable, or keys are not enforced.
	bad.FKs[0].Deferrable = true
	if _, err := orderTables([]*table{bad}, true); err != nil {
		t.Fatal(err)
	}
	bad.FKs[0].Deferrable = false
	if _, err := orderTables([]*table{bad}, false); err != nil {
		t.Fatal(err)
	}
	// A NOT NULL cycle is refused on PostgreSQL, broken on SQLite.
	a := mk("a", []string{"id"}, map[string]bool{"id": true, "b": true}, fk("b", "b", false))
	b := mk("b", []string{"id"}, map[string]bool{"id": true, "a": true}, fk("a", "a", false))
	if _, err := orderTables([]*table{a, b}, true); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err = %v", err)
	}
	if p, err := orderTables([]*table{a, b}, false); err != nil || len(p.Tables) != 2 {
		t.Fatalf("sqlite order = %v %v", p.Tables, err)
	}
}

func names(ts []*table) string {
	n := make([]string, len(ts))
	for i, t := range ts {
		n[i] = t.Name
	}
	return strings.Join(n, ",")
}

func TestValueHelpers(t *testing.T) {
	for in, want := range map[string]string{"12.500000000": "12.5", "3.000000000": "3", "-0.000000000": "0", "0": "0",
		"-0.000000001": "-0.000000001", "100": "100", "1e5": "1e5"} {
		if got := normalizeDecimal(in); got != want {
			t.Errorf("normalizeDecimal(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"RANGE (started_at)": "started_at", `RANGE ("Weird")`: "Weird", "RANGE (a, b)": "", "LIST (lower(x))": ""} {
		if got := partitionColumn(in); got != want {
			t.Errorf("partitionColumn(%q) = %q, want %q", in, got, want)
		}
	}
	tm, err := parseTime("2026-10-09 01:02:03.5")
	if err != nil || !tm.Equal(time.Date(2026, 10, 9, 1, 2, 3, 500_000_000, time.UTC)) {
		t.Fatalf("parseTime = %v %v", tm, err)
	}
	if v, err := toSQLite(true, kBool); err != nil || v != int64(1) {
		t.Fatalf("bool = %v %v", v, err)
	}
	if v, _ := toSQLite(tm, kTime); v != "2026-10-09T01:02:03.500000000Z" {
		t.Fatalf("time = %v", v)
	}
	if v, err := fromSQLite("2D1E7B3C-0000-4000-8000-000000000001", kUUID); err != nil || v != "2d1e7b3c-0000-4000-8000-000000000001" {
		t.Fatalf("uuid = %v %v", v, err)
	}
	if _, err := toPG("{bad", column{Kind: kJSON}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
