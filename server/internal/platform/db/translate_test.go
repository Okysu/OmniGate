package db

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTranslateSQLite(t *testing.T) {
	cases := []struct{ in, want string }{
		{`SELECT $1`, `SELECT ?1`},
		{`SELECT $2, $1, $2, $10`, `SELECT ?2, ?1, ?2, ?10`},
		{`WHERE id = ANY($3)`, `WHERE id IN (SELECT value FROM json_each(?3))`},
		{`WHERE id = any ( $1::uuid[] )`, `WHERE id IN (SELECT value FROM json_each(?1))`},
		{`SELECT used::text, '{}'::jsonb, $1::timestamptz`, `SELECT used, '{}', ?1`},
		{`SELECT x FROM t WHERE id = $1 FOR UPDATE`, `SELECT x FROM t WHERE id = ?1 `},
		{`SELECT x FROM t p WHERE id = $1 FOR UPDATE OF p`, `SELECT x FROM t p WHERE id = ?1 `},
		{`SELECT status FROM plans WHERE id = $1 FOR SHARE`, `SELECT status FROM plans WHERE id = ?1 `},
		{`UPDATE t SET at = now()`, `UPDATE t SET at = ` + sqliteNow},
		{`SET r = GREATEST(r - $2, 0), s = least(a, b)`, `SET r = max(r - ?2, 0), s = min(a, b)`},
		{`SELECT jsonb_array_length(risk)`, `SELECT json_array_length(risk)`},
		// literals, quoted identifiers and comments are untouched
		{`SELECT '$1 ::text now() FOR UPDATE', "a$1" -- $2 now()` + "\n" + `, $3 /* $4 */`,
			`SELECT '$1 ::text now() FOR UPDATE', "a$1" -- $2 now()` + "\n" + `, ?3 /* $4 */`},
		{`SELECT 'it''s $1', $1`, `SELECT 'it''s $1', ?1`},
		{`WHERE name LIKE $1 ESCAPE '\'`, `WHERE name LIKE ?1 ESCAPE '\'`},
	}
	for _, c := range cases {
		if got := translateSQLite(c.in); got != c.want {
			t.Errorf("translate(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestIsWrite(t *testing.T) {
	for q, want := range map[string]bool{
		"SELECT 1": false, " insert into t values (1)": true, "UPDATE t SET a = 1": true,
		"DELETE FROM t": true, "WITH d AS (DELETE FROM t RETURNING a) SELECT 1": true,
		"WITH x AS (SELECT 1) SELECT * FROM x": false, "SELECT 'DELETE'": false,
	} {
		if got := isWrite(q); got != want {
			t.Errorf("isWrite(%q) = %v", q, got)
		}
	}
}

type named string
type amount int64

func TestSQLiteValue(t *testing.T) {
	id := uuid.MustParse("0192f0c4-1a2b-7c3d-8e4f-001122334455")
	at := time.Date(2026, 10, 8, 12, 0, 0, 1500, time.FixedZone("x", 8*3600))
	var nilID *uuid.UUID
	var nilSlice []string
	cases := []struct {
		in   any
		want any
	}{
		{nil, nil},
		{id, id.String()},
		{&id, id.String()},
		{nilID, nil},
		{at, "2026-10-08T04:00:00.000001500Z"},
		{&at, "2026-10-08T04:00:00.000001500Z"},
		{named("x"), "x"},
		{amount(-5), int64(-5)},
		{int32(7), int64(7)},
		{true, true},
		{[]string{"a", "b"}, `["a","b"]`},
		{[]uuid.UUID{id}, `["` + id.String() + `"]`},
		{nilSlice, nil},
		{map[string]any{"k": 1}, `{"k":1}`},
		{struct {
			A int `json:"a"`
		}{1}, `{"a":1}`},
		{json.RawMessage(`{"a":1}`), `{"a":1}`},
		{[]byte(`[1,2]`), `[1,2]`},
		{[]byte{0xff, 0x00, 0x10}, []byte{0xff, 0x00, 0x10}},
	}
	for _, c := range cases {
		got, err := sqliteValue(c.in)
		if err != nil {
			t.Fatalf("sqliteValue(%#v): %v", c.in, err)
		}
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(c.want)
		if string(gb) != string(wb) {
			t.Errorf("sqliteValue(%#v) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestTimeTextOrder(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(time.Nanosecond), base.Add(999 * time.Millisecond), base.Add(time.Second),
		base.Add(time.Hour), base.AddDate(1, 0, 0)}
	for i := 1; i < len(times); i++ {
		if !(TimeText(times[i-1]) < TimeText(times[i])) {
			t.Fatalf("%s !< %s", TimeText(times[i-1]), TimeText(times[i]))
		}
	}
	if len(TimeText(base)) != len(TimeText(base.Add(123*time.Nanosecond))) {
		t.Fatal("TimeText must be fixed width")
	}
}
