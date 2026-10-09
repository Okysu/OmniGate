package transfer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
)

// requireDB skips t unless integration tests are enabled
// (OMNIGATE_TEST_DATABASE_URL set to a PostgreSQL URL or "sqlite").
func requireDB(t testing.TB) {
	t.Helper()
	if strings.TrimSpace(os.Getenv(dbtest.Env)) == "" {
		t.Skip(dbtest.Env + " not set; skipping integration test")
	}
}

// havePostgres reports whether the test database server is PostgreSQL.
func havePostgres() bool {
	v := strings.TrimSpace(os.Getenv(dbtest.Env))
	return v != "" && v != "sqlite" && !db.IsSQLiteURL(v)
}

// sqliteURL returns a new SQLite database URL in t's temporary directory.
func sqliteURL(t testing.TB) string {
	t.Helper()
	requireDB(t)
	return "sqlite://" + filepath.Join(t.TempDir(), "omnigate.db")
}

// pgURL returns a new scratch PostgreSQL database (dropped after t), skipping
// t when the test server is not PostgreSQL.
func pgURL(t testing.TB) string {
	t.Helper()
	requireDB(t)
	if !havePostgres() {
		t.Skip("PostgreSQL test database not configured")
	}
	return dbtest.URL(t)
}

func urlFor(t testing.TB, d db.Dialect) string {
	if d == db.Postgres {
		return pgURL(t)
	}
	return sqliteURL(t)
}

// openRaw opens u through the db layer without migrating.
func openRaw(t testing.TB, u string) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

func mustExec(t testing.TB, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// dump reads every table of the database at u into canonical, sorted row
// strings. kinds gives the logical kind of each column (from a PostgreSQL
// schema); columns without one are compared as raw values.
func dump(t testing.TB, u string, kinds map[string]map[string]kind) map[string][]string {
	t.Helper()
	ctx := context.Background()
	e, err := parseEndpoint(u)
	if err != nil {
		t.Fatal(err)
	}
	r, err := openReader(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	tables, err := describe(ctx, e.dialect, r.Query)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, tb := range tables {
		cols := slices.Clone(tb.Columns)
		slices.SortFunc(cols, func(a, b column) int { return strings.Compare(a.Name, b.Name) })
		exprs := make([]string, len(cols))
		for i, c := range cols {
			if k, ok := kinds[tb.Name][c.Name]; ok {
				cols[i].Kind = k
			}
			if e.dialect == db.Postgres {
				exprs[i] = pgSelectExpr(cols[i])
			} else {
				exprs[i] = quoteIdent(c.Name)
			}
		}
		rows := []string{}
		err := r.Stream(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM "+quoteIdent(tb.Name), func(raw []any) error {
			parts := make([]string, len(raw))
			for i, v := range raw {
				var err error
				if e.dialect == db.Postgres {
					v, err = fromPG(v, cols[i].Kind)
				} else {
					v, err = fromSQLite(v, cols[i].Kind)
				}
				if err != nil {
					return fmt.Errorf("%s.%s: %w", tb.Name, cols[i].Name, err)
				}
				parts[i] = cols[i].Name + "=" + canonical(v, cols[i].Kind)
			}
			rows = append(rows, strings.Join(parts, " | "))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(rows)
		out[tb.Name] = rows
	}
	return out
}

func canonical(v any, k kind) string {
	if v == nil {
		return "NULL"
	}
	switch k {
	case kTime:
		return v.(time.Time).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	case kDate:
		return v.(time.Time).Format(time.DateOnly)
	case kJSON, kArray:
		dec := json.NewDecoder(strings.NewReader(v.(string)))
		dec.UseNumber()
		var x any
		if err := dec.Decode(&x); err != nil {
			return "invalid json " + v.(string)
		}
		b, _ := json.Marshal(x)
		return string(b)
	case kDecimal:
		return normalizeDecimal(v.(string))
	case kFloat:
		return strconv.FormatFloat(v.(float64), 'g', -1, 64)
	}
	switch x := v.(type) {
	case []byte:
		return "x'" + hex.EncodeToString(x) + "'"
	case string:
		return strconv.Quote(x)
	}
	return fmt.Sprint(v)
}

// pgKinds returns the column kinds of the PostgreSQL database at u.
func pgKinds(t testing.TB, u string) map[string]map[string]kind {
	t.Helper()
	ctx := context.Background()
	e, _ := parseEndpoint(u)
	r, err := openReader(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	tables, err := describePostgres(ctx, r.Query)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]kind{}
	for _, tb := range tables {
		out[tb.Name] = map[string]kind{}
		for _, c := range tb.Columns {
			out[tb.Name][c.Name] = c.Kind
		}
	}
	return out
}

// sameData fails t when the two databases hold different data. kinds comes
// from whichever database is PostgreSQL (nil for SQLite → SQLite).
func sameData(t testing.TB, a, b string) {
	t.Helper()
	var kinds map[string]map[string]kind
	switch {
	case !db.IsSQLiteURL(b):
		kinds = pgKinds(t, b)
	case !db.IsSQLiteURL(a):
		kinds = pgKinds(t, a)
	}
	da, dbb := dump(t, a, kinds), dump(t, b, kinds)
	for name, rows := range da {
		other, ok := dbb[name]
		if !ok {
			t.Errorf("table %s missing in target", name)
			continue
		}
		if len(rows) != len(other) {
			t.Errorf("table %s: %d rows vs %d", name, len(rows), len(other))
			continue
		}
		for i := range rows {
			if rows[i] != other[i] {
				t.Errorf("table %s differs:\n  source: %s\n  target: %s", name, rows[i], other[i])
				break
			}
		}
	}
	for name := range dbb {
		if _, ok := da[name]; !ok {
			t.Errorf("table %s only in target", name)
		}
	}
}
