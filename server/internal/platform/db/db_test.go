package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
)

// backends returns a fresh SQLite database (always) and a PostgreSQL one when
// OMNIGATE_TEST_DATABASE_URL points at PostgreSQL, each with a scratch table.
func backends(t *testing.T) map[string]*db.DB {
	t.Helper()
	ctx := context.Background()
	out := map[string]*db.DB{}
	sq, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sq.Close)
	out["sqlite"] = sq
	if u := os.Getenv(dbtest.Env); u != "" && !db.IsSQLiteURL(u) && u != "sqlite" {
		pg, err := db.Open(ctx, dbtest.URL(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pg.Close)
		out["postgres"] = pg
	}
	for name, d := range out {
		ddl := `CREATE TABLE parents (id uuid PRIMARY KEY, name text NOT NULL UNIQUE)`
		ddl2 := `CREATE TABLE things (
			id uuid PRIMARY KEY, parent_id uuid REFERENCES parents (id), label text NOT NULL, nick text,
			flag boolean NOT NULL, n bigint NOT NULL, maybe_n integer, at timestamptz NOT NULL, maybe_at timestamptz,
			doc jsonb NOT NULL, list jsonb, hash bytea, used numeric(38, 9))`
		if d.Dialect() == db.SQLite {
			ddl = `CREATE TABLE parents (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE) STRICT`
			ddl2 = `CREATE TABLE things (
				id TEXT PRIMARY KEY, parent_id TEXT REFERENCES parents (id), label TEXT NOT NULL, nick TEXT,
				flag INTEGER NOT NULL, n INTEGER NOT NULL, maybe_n INTEGER, at TEXT NOT NULL, maybe_at TEXT,
				doc TEXT NOT NULL CHECK (json_valid(doc)), list TEXT, hash BLOB, used TEXT) STRICT`
		}
		for _, q := range []string{ddl, ddl2} {
			if _, err := d.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}
	return out
}

type doc struct {
	Name  string         `json:"name"`
	Tags  []string       `json:"tags"`
	Extra map[string]int `json:"extra"`
}

type label string
type amount int64

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	for name, d := range backends(t) {
		t.Run(name, func(t *testing.T) {
			pid, id := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			at := time.Date(2026, 10, 8, 12, 34, 56, 123456000, time.UTC)
			if _, err := d.Exec(ctx, `INSERT INTO parents (id, name) VALUES ($1, $2)`, pid, "p"); err != nil {
				t.Fatal(err)
			}
			in := doc{Name: "x", Tags: []string{"a"}, Extra: map[string]int{"k": 1}}
			raw, _ := json.Marshal(in)
			hash := []byte{0xde, 0xad, 0x00, 0xbe, 0xef}
			_, err := d.Exec(ctx, `INSERT INTO things (id, parent_id, label, nick, flag, n, maybe_n, at, maybe_at, doc, list, hash, used)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				id, &pid, label("hello"), nil, true, amount(-42), nil, at, nil, raw, []string{"x", "y"}, hash, "12.5")
			if err != nil {
				t.Fatal(err)
			}
			var (
				gotID    uuid.UUID
				gotPID   *uuid.UUID
				gotLabel label
				nick     *string
				flag     bool
				n        amount
				maybeN   *int32
				gotAt    time.Time
				maybeAt  *time.Time
				gotDoc   doc
				list     []string
				gotHash  []byte
				used     string
				rawDoc   json.RawMessage
				docMap   map[string]any
			)
			err = d.QueryRow(ctx, `SELECT id, parent_id, label, nick, flag, n, maybe_n, at, maybe_at, doc, list, hash, used::text, doc, doc
				FROM things WHERE id = $1`, id).Scan(&gotID, &gotPID, &gotLabel, &nick, &flag, &n, &maybeN, &gotAt, &maybeAt,
				&gotDoc, &list, &gotHash, &used, &rawDoc, &docMap)
			if err != nil {
				t.Fatal(err)
			}
			if gotID != id || gotPID == nil || *gotPID != pid || gotLabel != "hello" || nick != nil || !flag || n != -42 ||
				maybeN != nil || !gotAt.Equal(at) || maybeAt != nil || list[1] != "y" || string(gotHash) != string(hash) ||
				strings.TrimRight(used, "0") != "12.5" || docMap["name"] != "x" {
				t.Fatalf("round trip mismatch: %v %v %v %v %v %v %v %v %v %v %v %q", gotID, gotPID, gotLabel, nick, flag, n, maybeN,
					gotAt, maybeAt, list, gotHash, used)
			}
			if gotDoc.Name != "x" || gotDoc.Tags[0] != "a" || gotDoc.Extra["k"] != 1 {
				t.Fatalf("doc = %+v", gotDoc)
			}
			var back doc
			if err := json.Unmarshal(rawDoc, &back); err != nil || back.Name != "x" {
				t.Fatalf("raw doc %s: %v", rawDoc, err)
			}

			// ANY over a uuid slice, NULL handling, repeated and out-of-order placeholders.
			var cnt int
			if err := d.QueryRow(ctx, `SELECT count(*) FROM things WHERE id = ANY($2) AND label = $1 AND label = $1`,
				"hello", []uuid.UUID{id, uuid.Must(uuid.NewV7())}).Scan(&cnt); err != nil || cnt != 1 {
				t.Fatalf("ANY: %d %v", cnt, err)
			}
			if err := d.QueryRow(ctx, `SELECT count(*) FROM things WHERE id = ANY($1)`, []uuid.UUID{}).Scan(&cnt); err != nil || cnt != 0 {
				t.Fatalf("ANY empty: %d %v", cnt, err)
			}
			var a, b string
			if err := d.QueryRow(ctx, `SELECT $2::text, $1::text`, "one", "two").Scan(&a, &b); err != nil || a != "two" || b != "one" {
				t.Fatalf("out of order: %q %q %v", a, b, err)
			}

			// ErrNoRows, RowsAffected, RETURNING.
			if err := d.QueryRow(ctx, `SELECT label FROM things WHERE id = $1`, uuid.New()).Scan(&a); !db.IsNoRows(err) || !errors.Is(err, db.ErrNoRows) {
				t.Fatalf("want no rows, got %v", err)
			}
			tag, err := d.Exec(ctx, `UPDATE things SET nick = $2, maybe_at = $3 WHERE id = $1`, id, "nick", at)
			if err != nil || tag.RowsAffected() != 1 {
				t.Fatalf("update: %v", err)
			}
			if err := d.QueryRow(ctx, `UPDATE things SET n = n + 1 WHERE id = $1 RETURNING n, maybe_at`, id).Scan(&n, &maybeAt); err != nil ||
				n != -41 || maybeAt == nil || !maybeAt.Equal(at) {
				t.Fatalf("returning: %v %v %v", n, maybeAt, err)
			}

			// Constraint errors.
			_, err = d.Exec(ctx, `INSERT INTO parents (id, name) VALUES ($1, $2)`, uuid.New(), "p")
			if !db.IsUniqueViolation(err) || db.IsForeignKeyViolation(err) {
				t.Fatalf("want unique violation, got %v", err)
			}
			_, err = d.Exec(ctx, `INSERT INTO parents (id, name) VALUES ($1, $2)`, pid, "other")
			if !db.IsUniqueViolation(err) {
				t.Fatalf("want primary key violation, got %v", err)
			}
			_, err = d.Exec(ctx, `INSERT INTO things (id, parent_id, label, flag, n, at, doc) VALUES ($1, $2, 'l', false, 0, $3, '{}')`,
				uuid.New(), uuid.New(), at)
			if !db.IsForeignKeyViolation(err) || db.IsUniqueViolation(err) {
				t.Fatalf("want foreign key violation, got %v", err)
			}

			// Transactions roll back on error.
			boom := errors.New("boom")
			err = db.InTx(ctx, d, func(tx db.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE things SET label = 'changed' WHERE id = $1`, id); err != nil {
					return err
				}
				return boom
			})
			if !errors.Is(err, boom) {
				t.Fatal(err)
			}
			if err := d.QueryRow(ctx, `SELECT label FROM things WHERE id = $1 FOR UPDATE`, id).Scan(&a); err != nil || a != "hello" {
				t.Fatalf("rollback: %q %v", a, err)
			}

			// CopyFrom.
			var rows [][]any
			for i := 0; i < 50; i++ {
				rows = append(rows, []any{uuid.Must(uuid.NewV7()), fmt.Sprintf("c%d", i), i%2 == 0, int64(i), at.Add(time.Duration(i) * time.Second), `{"i":1}`})
			}
			if _, err := d.CopyFrom(ctx, "things", []string{"id", "label", "flag", "n", "at", "doc"}, rows); err != nil {
				t.Fatal(err)
			}
			ids, err := d.Query(ctx, `SELECT id FROM things WHERE label LIKE 'c%' ORDER BY at DESC`)
			if err != nil {
				t.Fatal(err)
			}
			got, err := db.CollectRows[uuid.UUID](ids)
			if err != nil || len(got) != 50 || got[0] != rows[49][0].(uuid.UUID) {
				t.Fatalf("copy: %d %v", len(got), err)
			}
		})
	}
}

// TestAggregatesMatch runs the Dialect helpers on both databases with the
// same data and expects identical results.
func TestAggregatesMatch(t *testing.T) {
	ctx := context.Background()
	results := map[string]string{}
	for name, d := range backends(t) {
		base := time.Date(2026, 10, 7, 23, 59, 59, 999999000, time.UTC)
		for i, v := range []int64{9223372036854775807, 10, 3, 7, 1} {
			if _, err := d.Exec(ctx, `INSERT INTO things (id, label, flag, n, maybe_n, at, doc) VALUES ($1, 'x', true, $2, $3, $4, '{}')`,
				uuid.Must(uuid.NewV7()), v, i*10, base.Add(time.Duration(i)*time.Microsecond)); err != nil {
				t.Fatal(err)
			}
		}
		dl := d.Dialect()
		var sum, empty *string
		var p50, p95, p0 *float64
		var none *float64
		err := d.QueryRow(ctx, `SELECT `+dl.SumText("n")+`, `+dl.Percentile(0.5, "maybe_n")+`, `+dl.Percentile(0.95, "maybe_n")+`, `+
			dl.Percentile(0, "maybe_n")+`, (SELECT `+dl.SumText("n")+` FROM things WHERE false), (SELECT `+dl.Percentile(0.5, "maybe_n")+
			` FROM things WHERE false) FROM things`).Scan(&sum, &p50, &p95, &p0, &empty, &none)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rows, err := d.Query(ctx, `SELECT `+dl.DateUTC("at")+` AS d, count(*) FROM things GROUP BY d ORDER BY d`)
		if err != nil {
			t.Fatal(err)
		}
		var days []string
		for rows.Next() {
			var day string
			var n int
			if err := rows.Scan(&day, &n); err != nil {
				t.Fatal(err)
			}
			days = append(days, fmt.Sprintf("%s=%d", day, n))
		}
		rows.Close()
		results[name] = fmt.Sprintf("sum=%s p50=%v p95=%v p0=%v empty=%v none=%v days=%v", *sum, *p50, *p95, *p0, empty, none, days)
		if *sum != "9223372036854775828" || *p50 != 20 || *p95 != 38 || *p0 != 0 || empty != nil || none != nil {
			t.Errorf("%s: %s", name, results[name])
		}
	}
	if len(results) == 2 && results["sqlite"] != results["postgres"] {
		t.Errorf("dialects differ:\n%s\n%s", results["sqlite"], results["postgres"])
	}
}

func TestJSONArrayElements(t *testing.T) {
	ctx := context.Background()
	for name, d := range backends(t) {
		if _, err := d.Exec(ctx, `INSERT INTO things (id, label, flag, n, at, doc) VALUES ($1, 'j', true, 0, $2, $3)`,
			uuid.New(), time.Now(), `[{"id":"a","window":{"kind":"rolling"}},{"id":"b","window":{"kind":"calendar"}}]`); err != nil {
			t.Fatal(err)
		}
		agg := "string_agg(r.value->>'id', ',')"
		if d.Dialect() == db.SQLite {
			agg = "group_concat(r.value->>'id', ',')"
		}
		var ids string
		err := d.QueryRow(ctx, `SELECT `+agg+` FROM things t, `+d.Dialect().JSONArrayElements("t.doc", "r")+
			` WHERE r.value->'window'->>'kind' IN ('rolling', 'session')`).Scan(&ids)
		if err != nil || ids != "a" {
			t.Fatalf("%s: %q %v", name, ids, err)
		}
	}
}

// TestSQLiteTimeOrdering checks that stored timestamps compare like times,
// including the now() translation and SQLite's own column defaults.
func TestSQLiteTimeOrdering(t *testing.T) {
	ctx := context.Background()
	d := backends(t)["sqlite"]
	base := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	offsets := []time.Duration{time.Hour, time.Nanosecond, 0, 999 * time.Millisecond, 59 * time.Second, -time.Nanosecond}
	for i, off := range offsets {
		// Mixed input zones must not matter.
		at := base.Add(off).In(time.FixedZone("z", (i-3)*3600))
		if _, err := d.Exec(ctx, `INSERT INTO things (id, label, flag, n, at, doc) VALUES ($1, 't', false, $2, $3, '{}')`,
			uuid.New(), int64(i), at); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.Query(ctx, `SELECT at FROM things WHERE at >= $1 ORDER BY at`, base)
	if err != nil {
		t.Fatal(err)
	}
	var prev time.Time
	n := 0
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		if at.Location() != time.UTC || at.Before(prev) || at.Before(base) {
			t.Fatalf("bad order or zone: %v after %v", at, prev)
		}
		prev, n = at, n+1
	}
	if n != 5 {
		t.Fatalf("got %d rows >= base, want 5", n)
	}
	var later bool
	if err := d.QueryRow(ctx, `SELECT now() > $1`, time.Now().Add(-time.Minute)).Scan(&later); err != nil || !later {
		t.Fatalf("now(): %v %v", later, err)
	}
	var now time.Time
	if err := d.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil || time.Since(now) > time.Minute {
		t.Fatalf("now() = %v, %v", now, err)
	}
}

// TestSQLiteConcurrentWriters runs many concurrent transactions and
// autocommit writes; the write lock and busy timeout must serialize them
// without "database is locked" errors or lost updates.
func TestSQLiteConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	d := backends(t)["sqlite"]
	id := uuid.New()
	if _, err := d.Exec(ctx, `INSERT INTO things (id, label, flag, n, at, doc) VALUES ($1, 'c', false, 0, $2, '{}')`, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A second handle on the same file stands in for another process: its
	// writes are only serialized by SQLite's busy timeout.
	other, err := db.Open(ctx, "sqlite://"+d.SQLPath())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	const workers, each = 16, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				var err error
				switch {
				case w%4 == 3:
					_, err = other.Exec(ctx, `UPDATE things SET n = n + 1 WHERE id = $1`, id)
				case w%2 == 0:
					err = db.InTx(ctx, d, func(tx db.Tx) error {
						var n int64
						if err := tx.QueryRow(ctx, `SELECT n FROM things WHERE id = $1 FOR UPDATE`, id).Scan(&n); err != nil {
							return err
						}
						// Read-modify-write: only correct if transactions are serialized.
						_, err := tx.Exec(ctx, `UPDATE things SET n = $2 WHERE id = $1`, id, n+1)
						return err
					})
				default:
					_, err = d.Exec(ctx, `UPDATE things SET n = n + 1 WHERE id = $1`, id)
				}
				if err != nil {
					errs <- err
				}
				var n int64
				if err := d.QueryRow(ctx, `SELECT n FROM things WHERE id = $1`, id).Scan(&n); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var n int64
	if err := d.QueryRow(ctx, `SELECT n FROM things WHERE id = $1`, id).Scan(&n); err != nil || n != workers*each {
		t.Fatalf("n = %d, want %d (%v)", n, workers*each, err)
	}
}

func TestSQLiteURLs(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := os.Stat(filepath.Join(dir, "x.db")); err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	// Relative path.
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	d, err = db.Open(ctx, "sqlite://rel/omnigate.db")
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := os.Stat("rel/omnigate.db"); err != nil {
		t.Fatal(err)
	}
	// In-memory: one database shared by all pool connections.
	m, err := db.Open(ctx, "sqlite::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Exec(ctx, `CREATE TABLE t (v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	err = db.InTx(ctx, m, func(tx db.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO t VALUES (1)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var n int
			if err := m.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n); err != nil || n != 1 {
				t.Errorf("memory db: %d %v", n, err)
			}
		}()
	}
	wg.Wait()
	if _, err := db.Open(ctx, "sqlite:"); err == nil {
		t.Fatal("empty sqlite path must fail")
	}
}

// TestMigrations applies every migration, rolls all of them back and applies
// them again on SQLite (and PostgreSQL when configured).
func TestMigrations(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	urls := map[string]string{"sqlite": "sqlite://" + filepath.Join(t.TempDir(), "m.db")}
	if u := os.Getenv(dbtest.Env); u != "" && !strings.HasPrefix(u, "sqlite") {
		urls["postgres"] = dbtest.URL(t)
	}
	for name, u := range urls {
		t.Run(name, func(t *testing.T) {
			d, err := db.Open(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if err := db.Migrate(ctx, d, log); err != nil {
				t.Fatal(err)
			}
			st, err := db.MigrationStatus(ctx, d)
			if err != nil || st.Pending != 0 || st.Current != st.Latest || st.Latest < 8 || st.Dialect != name {
				t.Fatalf("status %+v %v", st, err)
			}
			if err := db.MigrateDownTo(ctx, d, 0); err != nil {
				t.Fatalf("down: %v", err)
			}
			if st, _ := db.MigrationStatus(ctx, d); st.Current != 0 {
				t.Fatalf("after down: %+v", st)
			}
			if err := db.Migrate(ctx, d, log); err != nil {
				t.Fatalf("up again: %v", err)
			}
			// A database migrated by a newer release must be refused (downgrade
			// protection): simulate it by recording a future version.
			if _, err := d.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, st.Latest+1); err != nil {
				t.Fatal(err)
			}
			if st, _ := db.MigrationStatus(ctx, d); !st.TooNew {
				t.Fatalf("status should be tooNew: %+v", st)
			}
			if err := db.CheckSchemaVersion(ctx, d); !errors.Is(err, db.ErrSchemaTooNew) {
				t.Fatalf("check = %v", err)
			}
			if err := db.Migrate(ctx, d, log); !errors.Is(err, db.ErrSchemaTooNew) {
				t.Fatalf("migrate on newer schema = %v", err)
			}
		})
	}
}

func TestSQLiteBackup(t *testing.T) {
	ctx := context.Background()
	d := backends(t)["sqlite"]
	if _, err := d.Exec(ctx, `INSERT INTO parents (id, name) VALUES ($1, 'b')`, uuid.New()); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "backups", "copy.db")
	if err := db.Backup(ctx, d, out); err != nil {
		t.Fatal(err)
	}
	if err := db.Backup(ctx, d, out); err == nil {
		t.Fatal("backup must not overwrite")
	}
	c, err := db.Open(ctx, "sqlite://"+out)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.QueryRow(ctx, `SELECT count(*) FROM parents`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("backup rows = %d %v", n, err)
	}
}
