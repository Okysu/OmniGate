package db

import (
	"fmt"
	"strconv"
	"time"
)

// Dialect identifies the SQL dialect of a database.
type Dialect int

const (
	Postgres Dialect = iota + 1
	SQLite
)

func (d Dialect) String() string {
	switch d {
	case Postgres:
		return "postgres"
	case SQLite:
		return "sqlite"
	}
	return "unknown"
}

// The helpers below cover the constructs the SQLite translator cannot rewrite
// mechanically. They return SQL fragments; column expressions are trusted
// (never user input).

// Percentile returns the continuous percentile (linear interpolation, NULLs
// ignored, NULL for no rows) of expr: percentile_cont on PostgreSQL, an
// application-defined aggregate with the same semantics on SQLite.
func (d Dialect) Percentile(fraction float64, expr string) string {
	f := strconv.FormatFloat(fraction, 'f', -1, 64)
	if d == SQLite {
		return fmt.Sprintf("og_percentile_cont(%s, %s)", expr, f)
	}
	return fmt.Sprintf("percentile_cont(%s) WITHIN GROUP (ORDER BY %s)", f, expr)
}

// SumText returns the exact sum of an integer expression as decimal text
// (NULL for no rows): sum()::text on PostgreSQL (numeric, never overflows),
// an arbitrary-precision aggregate on SQLite.
func (d Dialect) SumText(expr string) string {
	if d == SQLite {
		return "og_sum_text(" + expr + ")"
	}
	return "sum(" + expr + ")::text"
}

// DateUTC returns the UTC calendar date (YYYY-MM-DD text) of a timestamp
// expression.
func (d Dialect) DateUTC(expr string) string {
	if d == SQLite {
		return "strftime('%Y-%m-%d', " + expr + ")"
	}
	return "to_char(date_trunc('day', " + expr + " AT TIME ZONE 'UTC'), 'YYYY-MM-DD')"
}

// JSONArrayElements returns a FROM item expanding the JSON array expr into
// rows named alias whose element is alias.value (usable with ->, ->>).
func (d Dialect) JSONArrayElements(expr, alias string) string {
	if d == SQLite {
		return "json_each(" + expr + ") AS " + alias
	}
	return "jsonb_array_elements(" + expr + ") AS " + alias + "(value)"
}

// TimeText formats t in the canonical text form used for SQLite timestamps:
// UTC RFC 3339 with exactly nine fractional digits, so string order equals
// time order. Use it for timestamps embedded in JSON parameters (json_each).
func TimeText(t time.Time) string { return t.UTC().Format(timeLayout) }

// timeLayout is the fixed-width canonical SQLite timestamp format.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// sqliteNow is the SQL expression for the current time in timeLayout
// (SQLite keeps millisecond precision for 'now').
const sqliteNow = "strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')"
