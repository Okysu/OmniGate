package transfer

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"omnigate/internal/platform/db"
)

// tablePlan maps one target table onto its source table.
type tablePlan struct {
	*table          // target table
	Cols   []column // target columns with the logical kind of each
	Money  []string // *_nano integer columns whose sums are verified
}

func (tp *tablePlan) names() []string {
	n := make([]string, len(tp.Cols))
	for i, c := range tp.Cols {
		n[i] = c.Name
	}
	return n
}

// buildPlans matches source and target tables and columns by name and fixes
// the logical kind of each column (from whichever side is PostgreSQL). Both
// schemas are at the same migration version, so any difference means schema
// drift and is refused rather than silently dropping data.
func buildPlans(src, dst []*table, srcDialect, dstDialect db.Dialect) (map[string]*tablePlan, error) {
	srcBy := map[string]*table{}
	for _, t := range src {
		srcBy[t.Name] = t
	}
	dstBy := map[string]bool{}
	var problems []string
	plans := map[string]*tablePlan{}
	for _, dt := range dst {
		dstBy[dt.Name] = true
		st, ok := srcBy[dt.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("table %s exists only in the target", dt.Name))
			continue
		}
		tp := &tablePlan{table: dt}
		seen := map[string]bool{}
		for _, dc := range dt.Columns {
			sc, ok := st.column(dc.Name)
			if !ok {
				problems = append(problems, fmt.Sprintf("column %s.%s exists only in the target", dt.Name, dc.Name))
				continue
			}
			seen[dc.Name] = true
			c := dc
			switch {
			case dstDialect == db.Postgres && srcDialect == db.Postgres:
				if sc.Kind != dc.Kind || sc.Elem != dc.Elem {
					problems = append(problems, fmt.Sprintf("column %s.%s is %s in the source but %s in the target", dt.Name, dc.Name, sc.Type, dc.Type))
					continue
				}
			case srcDialect == db.Postgres:
				c.Kind, c.Elem = sc.Kind, sc.Elem
			case dstDialect == db.SQLite:
				c.Kind = kRaw
			}
			// A SQLite side must store the kind in the matching STRICT type.
			for _, side := range []struct {
				d   db.Dialect
				col column
			}{{srcDialect, sc}, {dstDialect, dc}} {
				if side.d == db.SQLite && c.Kind != kRaw && side.col.Type != "ANY" && side.col.Type != c.Kind.sqliteStorage() {
					problems = append(problems, fmt.Sprintf("column %s.%s is %s on SQLite; expected %s for %s values",
						dt.Name, dc.Name, side.col.Type, c.Kind.sqliteStorage(), c.Kind))
				}
			}
			tp.Cols = append(tp.Cols, c)
			if strings.HasSuffix(c.Name, "_nano") && (c.Kind == kInt || (c.Kind == kRaw && c.Type == "INTEGER")) {
				tp.Money = append(tp.Money, c.Name)
			}
		}
		for _, sc := range st.Columns {
			if !seen[sc.Name] {
				if _, inDst := dt.column(sc.Name); !inDst {
					problems = append(problems, fmt.Sprintf("column %s.%s exists only in the source", dt.Name, sc.Name))
				}
			}
		}
		plans[dt.Name] = tp
	}
	for _, st := range src {
		if !dstBy[st.Name] {
			problems = append(problems, fmt.Sprintf("table %s exists only in the source", st.Name))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("source and target schemas differ:\n  %s", strings.Join(problems, "\n  "))
	}
	return plans, nil
}

// stats are the verified figures of one table.
type stats struct {
	Rows int64
	Sums map[string]string // money column → exact sum as decimal text
}

func tableStats(ctx context.Context, q queryFunc, d db.Dialect, tp *tablePlan) (stats, error) {
	exprs := []string{"count(*)"}
	for _, c := range tp.Money {
		if d == db.Postgres {
			exprs = append(exprs, "coalesce(sum("+quoteIdent(c)+"), 0)::text")
		} else {
			// SQLite's sum() of integers fails on overflow instead of rounding.
			exprs = append(exprs, "coalesce(sum("+quoteIdent(c)+"), 0)")
		}
	}
	rows, err := q(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM "+quoteIdent(tp.Name))
	if err != nil {
		return stats{}, fmt.Errorf("count %s: %w", tp.Name, err)
	}
	s := stats{Rows: toInt(rows[0][0]), Sums: map[string]string{}}
	for i, c := range tp.Money {
		v := rows[0][i+1]
		if n, ok := v.(int64); ok {
			s.Sums[c] = strconv.FormatInt(n, 10)
		} else {
			s.Sums[c] = str(v)
		}
	}
	return s, nil
}

// copier copies every table from a reader snapshot into a writer transaction.
type copier struct {
	src   reader
	dst   writer
	batch int
	out   io.Writer
	// partitions prepares partitioned target tables before the copy
	// (PostgreSQL request_logs); nil skips it.
	partitions func(ctx context.Context, tp *tablePlan) error
}

// Result summarises a migration.
type Result struct {
	Tables []TableResult
	Rows   int64
}

// TableResult is one copied table.
type TableResult struct {
	Name string
	Rows int64
	Sums map[string]string
}

// run copies and verifies. replace allows existing target rows (they are
// deleted inside the copy transaction).
func (c *copier) run(ctx context.Context) (*Result, error) {
	srcTables, err := describe(ctx, c.src.Dialect(), c.src.Query)
	if err != nil {
		return nil, fmt.Errorf("source schema: %w", err)
	}
	dstTables, err := describe(ctx, c.dst.Dialect(), c.dst.Query)
	if err != nil {
		return nil, fmt.Errorf("target schema: %w", err)
	}
	plans, err := buildPlans(srcTables, dstTables, c.src.Dialect(), c.dst.Dialect())
	if err != nil {
		return nil, err
	}
	enforced := c.dst.Dialect() == db.Postgres
	order, err := orderTables(dstTables, enforced)
	if err != nil {
		return nil, err
	}

	// Source figures from the same snapshot the rows are read from.
	want := map[string]stats{}
	for _, t := range order.Tables {
		s, err := tableStats(ctx, c.src.Query, c.src.Dialect(), plans[t.Name])
		if err != nil {
			return nil, fmt.Errorf("source: %w", err)
		}
		want[t.Name] = s
	}
	if c.partitions != nil {
		for _, t := range order.Tables {
			if t.Partitioned {
				if err := c.partitions(ctx, plans[t.Name]); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := c.dst.Begin(ctx); err != nil {
		return nil, fmt.Errorf("begin target transaction: %w", err)
	}
	defer c.dst.Rollback(ctx)
	if enforced && slices.ContainsFunc(dstTables, func(t *table) bool {
		return slices.ContainsFunc(t.FKs, func(fk foreignKey) bool { return fk.Deferrable })
	}) {
		if err := c.dst.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
			return nil, err
		}
	}
	if err := c.clear(ctx, order.Tables); err != nil {
		return nil, fmt.Errorf("clear target: %w", err)
	}
	fmt.Fprintf(c.out, "copying %d tables (batch %d)\n", len(order.Tables), c.batch)
	var pending []deferredRow
	for i, t := range order.Tables {
		start := time.Now()
		n, def, err := c.copyTable(ctx, plans[t.Name], order.Deferred[t.Name], want[t.Name].Rows)
		if err != nil {
			return nil, fmt.Errorf("copy %s: %w", t.Name, err)
		}
		pending = append(pending, def...)
		fmt.Fprintf(c.out, "  [%d/%d] %-32s %10d rows  %s\n", i+1, len(order.Tables), t.Name, n,
			time.Since(start).Round(time.Millisecond))
		if n != want[t.Name].Rows {
			return nil, fmt.Errorf("copy %s: read %d rows but the source snapshot counts %d", t.Name, n, want[t.Name].Rows)
		}
	}
	if len(pending) > 0 {
		fmt.Fprintf(c.out, "restoring %d deferred foreign key values\n", len(pending))
		for _, d := range pending {
			if err := c.dst.Exec(ctx, d.sql, d.args...); err != nil {
				return nil, fmt.Errorf("restore deferred foreign key of %s: %w", d.table, err)
			}
		}
	}
	if err := c.finish(ctx, order.Tables, plans); err != nil {
		return nil, err
	}
	if err := c.dst.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit target: %w", err)
	}

	// Verify what was committed.
	res := &Result{}
	var bad []string
	for _, t := range order.Tables {
		got, err := tableStats(ctx, c.dst.Query, c.dst.Dialect(), plans[t.Name])
		if err != nil {
			return nil, fmt.Errorf("verify: %w", err)
		}
		w := want[t.Name]
		if got.Rows != w.Rows {
			bad = append(bad, fmt.Sprintf("%s: %d rows in the source, %d in the target", t.Name, w.Rows, got.Rows))
		}
		for _, m := range plans[t.Name].Money {
			if got.Sums[m] != w.Sums[m] {
				bad = append(bad, fmt.Sprintf("%s.%s: sum %s in the source, %s in the target", t.Name, m, w.Sums[m], got.Sums[m]))
			}
		}
		res.Tables = append(res.Tables, TableResult{Name: t.Name, Rows: got.Rows, Sums: got.Sums})
		res.Rows += got.Rows
	}
	if len(bad) > 0 {
		return res, fmt.Errorf("verification failed:\n  %s", strings.Join(bad, "\n  "))
	}
	if c.dst.Dialect() == db.Postgres {
		_ = c.dst.Exec(ctx, "ANALYZE") // planner statistics; best effort (outside the transaction)
	}
	return res, nil
}

func describe(ctx context.Context, d db.Dialect, q queryFunc) ([]*table, error) {
	if d == db.Postgres {
		return describePostgres(ctx, q)
	}
	return describeSQLite(ctx, q)
}

// clear deletes existing target rows (migration seed rows, or everything when
// the empty check was disabled).
func (c *copier) clear(ctx context.Context, tables []*table) error {
	if len(tables) == 0 {
		return nil
	}
	if c.dst.Dialect() == db.Postgres {
		names := make([]string, len(tables))
		for i, t := range tables {
			names[i] = t.Name
		}
		return c.dst.Exec(ctx, "TRUNCATE TABLE "+quoteList(names))
	}
	for i := len(tables) - 1; i >= 0; i-- {
		if err := c.dst.Exec(ctx, "DELETE FROM "+quoteIdent(tables[i].Name)); err != nil {
			return err
		}
	}
	return nil
}

// deferredRow is a second-pass UPDATE restoring deferred foreign key values.
type deferredRow struct {
	table string
	sql   string
	args  []any
}

func (c *copier) copyTable(ctx context.Context, tp *tablePlan, deferred []foreignKey, expect int64) (int64, []deferredRow, error) {
	srcDialect, dstDialect := c.src.Dialect(), c.dst.Dialect()
	exprs := make([]string, len(tp.Cols))
	for i, col := range tp.Cols {
		if srcDialect == db.Postgres {
			exprs[i] = pgSelectExpr(col)
		} else {
			exprs[i] = quoteIdent(col.Name)
		}
	}
	// Deferred foreign key columns are written as NULL, then updated by key.
	var defIdx, keyIdx []int
	var update string
	if len(deferred) > 0 {
		set := map[string]bool{}
		for _, fk := range deferred {
			for _, col := range fk.Columns {
				set[col] = true
			}
		}
		var sets, where []string
		for i, col := range tp.Cols {
			if set[col.Name] {
				defIdx = append(defIdx, i)
				sets = append(sets, fmt.Sprintf("%s = $%d", quoteIdent(col.Name), len(defIdx)))
			}
		}
		for _, k := range tp.PK {
			for i, col := range tp.Cols {
				if col.Name == k {
					keyIdx = append(keyIdx, i)
					where = append(where, fmt.Sprintf("%s = $%d", quoteIdent(k), len(defIdx)+len(keyIdx)))
				}
			}
		}
		update = "UPDATE " + quoteIdent(tp.Name) + " SET " + strings.Join(sets, ", ") + " WHERE " + strings.Join(where, " AND ")
	}

	names := tp.names()
	var (
		n     int64
		batch = make([][]any, 0, c.batch)
		defs  []deferredRow
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := c.dst.Write(ctx, tp.Name, names, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	err := c.src.Stream(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM "+quoteIdent(tp.Name), func(raw []any) error {
		row := make([]any, len(raw))
		for i, v := range raw {
			col := tp.Cols[i]
			var err error
			if srcDialect == db.Postgres {
				v, err = fromPG(v, col.Kind)
			} else {
				v, err = fromSQLite(v, col.Kind)
			}
			if err == nil {
				if dstDialect == db.Postgres {
					v, err = toPG(v, col)
				} else {
					v, err = toSQLite(v, col.Kind)
				}
			}
			if err != nil {
				return fmt.Errorf("row %d, column %s: %w", n+1, col.Name, err)
			}
			row[i] = v
		}
		if len(defIdx) > 0 {
			args := make([]any, 0, len(defIdx)+len(keyIdx))
			has := false
			for _, i := range defIdx {
				args = append(args, row[i])
				has = has || row[i] != nil
				row[i] = nil
			}
			if has {
				for _, i := range keyIdx {
					args = append(args, row[i])
				}
				defs = append(defs, deferredRow{table: tp.Name, sql: update, args: args})
			}
		}
		batch = append(batch, row)
		n++
		if len(batch) >= c.batch {
			if err := flush(); err != nil {
				return err
			}
			if expect > int64(c.batch)*20 && n%(int64(c.batch)*20) == 0 {
				fmt.Fprintf(c.out, "        %s: %d / %d rows\n", tp.Name, n, expect)
			}
		}
		return nil
	})
	if err != nil {
		return n, nil, err
	}
	return n, defs, flush()
}

// finish runs the target's integrity steps before COMMIT: on SQLite
// PRAGMA foreign_key_check (foreign keys were off during the copy), on
// PostgreSQL resetting sequences of serial / identity columns.
func (c *copier) finish(ctx context.Context, tables []*table, plans map[string]*tablePlan) error {
	if c.dst.Dialect() == db.SQLite {
		rows, err := c.dst.Query(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return fmt.Errorf("foreign key check: %w", err)
		}
		if len(rows) > 0 {
			var lines []string
			for i, r := range rows {
				if i == 10 {
					lines = append(lines, fmt.Sprintf("… and %d more", len(rows)-10))
					break
				}
				lines = append(lines, fmt.Sprintf("%s rowid %v references a missing %s row", str(r[0]), r[1], str(r[2])))
			}
			return fmt.Errorf("foreign key check failed after copy:\n  %s", strings.Join(lines, "\n  "))
		}
		return nil
	}
	for _, t := range tables {
		for _, col := range plans[t.Name].Cols {
			if !col.Serial {
				continue
			}
			q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence($1, $2), coalesce((SELECT max(%s) FROM %s), 0) + 1, false)`,
				quoteIdent(col.Name), quoteIdent(t.Name))
			if err := c.dst.Exec(ctx, q, quoteIdent(t.Name), col.Name); err != nil {
				return fmt.Errorf("reset sequence of %s.%s: %w", t.Name, col.Name, err)
			}
		}
	}
	return nil
}
