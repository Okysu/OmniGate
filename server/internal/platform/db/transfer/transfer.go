// Package transfer copies all OmniGate data between two databases, in either
// direction between SQLite and PostgreSQL (`omnigate migrate-db`, see
// docs/contracts/phase8-api.md §4 and ADR-0009).
//
// Nothing here is specific to individual tables: the tables, their columns and
// the foreign-key order are discovered from the migrated schemas (pg_catalog
// on PostgreSQL, sqlite_master and pragmas on SQLite), and values are
// converted by column type. The only special case is the monthly partitioning
// of request_logs on PostgreSQL, whose partitions are created through
// requestlog.EnsurePartitions before the copy.
//
// Procedure: refuse databases newer than this binary; migrate both databases
// to the latest version; refuse a non-empty target; read the source inside one
// read-only snapshot; write the target inside one transaction (COPY on
// PostgreSQL, foreign keys off and PRAGMA foreign_key_check on SQLite); then
// compare per-table row counts and the sums of all *_nano money columns.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"omnigate/internal/platform/db"
	"omnigate/internal/requestlog"
	"omnigate/migrations"
)

// DefaultBatch is the default number of rows per COPY / INSERT batch.
const DefaultBatch = 1000

// Options configures Run.
type Options struct {
	From, To string // database URLs (OMNIGATE_DATABASE_URL syntax)
	Batch    int    // rows per batch (DefaultBatch when 0)
	// DryRun only checks versions and emptiness and counts source rows; neither
	// database is changed (not even migrated).
	DryRun bool
	// SkipEmptyCheck allows a target that already holds data; its rows are
	// deleted and replaced by the source's inside the copy transaction.
	SkipEmptyCheck bool
	Out            io.Writer    // progress and summary (io.Discard when nil)
	Log            *slog.Logger // migration log (discarded when nil)
}

// Run migrates the data from opts.From to opts.To. The returned Result is nil
// for a dry run.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Batch <= 0 {
		opts.Batch = DefaultBatch
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	src, err := parseEndpoint(opts.From)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	dst, err := parseEndpoint(opts.To)
	if err != nil {
		return nil, fmt.Errorf("--to: %w", err)
	}
	if src.same(dst) {
		return nil, errors.New("--from and --to name the same database")
	}
	if !src.exists() {
		return nil, fmt.Errorf("source: sqlite database %s does not exist", src.path)
	}
	out := opts.Out

	// Versions, read without changing anything.
	srcVer, err := inspectVersion(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	dstVer := int64(0)
	if dst.exists() {
		if dstVer, err = inspectVersion(ctx, dst); err != nil {
			return nil, fmt.Errorf("target: %w", err)
		}
	}
	srcLatest, err := latestVersion(src.dialect)
	if err != nil {
		return nil, err
	}
	dstLatest, err := latestVersion(dst.dialect)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "source: %s (schema version %d)\n", src, srcVer)
	fmt.Fprintf(out, "target: %s (schema version %d)\n", dst, dstVer)
	if srcLatest != dstLatest {
		return nil, fmt.Errorf("this binary's migrations disagree: postgres latest %d, sqlite latest %d", srcLatest, dstLatest)
	}
	if srcVer > srcLatest {
		return nil, fmt.Errorf("source schema version %d is newer than this omnigate binary supports (%d): use the omnigate version that wrote it", srcVer, srcLatest)
	}
	if dstVer > dstLatest {
		return nil, fmt.Errorf("target schema version %d is newer than this omnigate binary supports (%d)", dstVer, dstLatest)
	}

	ref, err := loadReference(ctx)
	if err != nil {
		return nil, fmt.Errorf("reference schema: %w", err)
	}
	if opts.DryRun {
		return nil, dryRun(ctx, opts, src, dst, srcVer, dstVer, srcLatest, ref)
	}

	// Migrate both to the latest version.
	for _, side := range []struct {
		name string
		e    endpoint
		ver  int64
	}{{"source", src, srcVer}, {"target", dst, dstVer}} {
		if side.ver == srcLatest {
			continue // up to date: not even opened for writing
		}
		fmt.Fprintf(out, "migrating %s from version %d to %d\n", side.name, side.ver, srcLatest)
		if err := migrate(ctx, side.e, opts.Log); err != nil {
			return nil, fmt.Errorf("migrate %s: %w", side.name, err)
		}
	}

	r, err := openReader(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	defer r.Close()
	w, err := openWriter(ctx, dst)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	defer w.Close()
	if v, err := schemaVersion(ctx, r.Query, src.dialect); err != nil || v != srcLatest {
		return nil, fmt.Errorf("source schema version is %d after migration, want %d (%v)", v, srcLatest, err)
	}
	if v, err := schemaVersion(ctx, w.Query, dst.dialect); err != nil || v != dstLatest {
		return nil, fmt.Errorf("target schema version is %d after migration, want %d (%v)", v, dstLatest, err)
	}

	dstTables, err := describe(ctx, dst.dialect, w.Query)
	if err != nil {
		return nil, fmt.Errorf("target schema: %w", err)
	}
	nonEmpty, err := nonEmptyTables(ctx, w.Query, dstTables, ref.seeds)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	if len(nonEmpty) > 0 {
		if !opts.SkipEmptyCheck {
			return nil, fmt.Errorf("target database is not empty: %s\n(migrate into a new, empty database; or pass --force-empty-check=false to delete and replace the target's data)",
				strings.Join(nonEmpty, ", "))
		}
		fmt.Fprintf(out, "WARNING: the target is not empty; its rows will be replaced: %s\n", strings.Join(nonEmpty, ", "))
	}

	c := &copier{src: r, dst: w, batch: opts.Batch, out: out}
	if dst.dialect == db.Postgres {
		c.partitions = func(ctx context.Context, tp *tablePlan) error {
			return preparePartitions(ctx, dst, r, tp, out, opts.Log)
		}
	}
	start := time.Now()
	res, err := c.run(ctx)
	if err != nil {
		return res, err
	}
	fmt.Fprintf(out, "verified: %d tables, %d rows; row counts and money sums match (%s)\n", len(res.Tables), res.Rows,
		time.Since(start).Round(time.Millisecond))
	for _, t := range res.Tables {
		if len(t.Sums) == 0 || t.Rows == 0 {
			continue
		}
		var parts []string
		for _, col := range slices.Sorted(maps.Keys(t.Sums)) {
			parts = append(parts, col+"="+t.Sums[col])
		}
		fmt.Fprintf(out, "  %-32s %s\n", t.Name, strings.Join(parts, " "))
	}
	fmt.Fprintln(out, "encrypted values (channel credentials, SMTP password, webhook secrets) were copied verbatim:")
	fmt.Fprintln(out, "start the target with the same OMNIGATE_MASTER_KEY as the source.")
	return res, nil
}

// dryRun reports what Run would do: versions, pending migrations, source row
// counts and the target's emptiness. It changes nothing.
func dryRun(ctx context.Context, opts Options, src, dst endpoint, srcVer, dstVer, latest int64, ref *reference) error {
	out := opts.Out
	for _, side := range []struct {
		name string
		ver  int64
	}{{"source", srcVer}, {"target", dstVer}} {
		if side.ver < latest {
			fmt.Fprintf(out, "would migrate %s from version %d to %d\n", side.name, side.ver, latest)
		}
	}
	r, err := openReader(ctx, src)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	defer r.Close()
	srcTables, err := describe(ctx, src.dialect, r.Query)
	if err != nil {
		return fmt.Errorf("source schema: %w", err)
	}
	order, err := orderTables(ref.tables, false)
	if err != nil {
		return err
	}
	exists := map[string]*table{}
	for _, t := range srcTables {
		exists[t.Name] = t
	}
	var total int64
	fmt.Fprintf(out, "source tables (%d):\n", len(order.Tables))
	for _, t := range order.Tables {
		st, ok := exists[t.Name]
		if !ok {
			fmt.Fprintf(out, "  %-32s %10s  (created by migration)\n", t.Name, "-")
			continue
		}
		s, err := tableStats(ctx, r.Query, src.dialect, &tablePlan{table: st})
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		total += s.Rows
		fmt.Fprintf(out, "  %-32s %10d rows\n", t.Name, s.Rows)
	}
	fmt.Fprintf(out, "source total: %d rows\n", total)

	var nonEmpty []string
	if dst.exists() {
		tr, err := openReader(ctx, dst)
		if err != nil {
			return fmt.Errorf("target: %w", err)
		}
		defer tr.Close()
		dstTables, err := describe(ctx, dst.dialect, tr.Query)
		if err != nil {
			return fmt.Errorf("target schema: %w", err)
		}
		if nonEmpty, err = nonEmptyTables(ctx, tr.Query, dstTables, ref.seeds); err != nil {
			return fmt.Errorf("target: %w", err)
		}
	}
	switch {
	case len(nonEmpty) == 0:
		fmt.Fprintln(out, "target: empty")
	case opts.SkipEmptyCheck:
		fmt.Fprintf(out, "target: NOT empty, its rows would be replaced: %s\n", strings.Join(nonEmpty, ", "))
	}
	fmt.Fprintln(out, "dry run: no changes made")
	if len(nonEmpty) > 0 && !opts.SkipEmptyCheck {
		return fmt.Errorf("target database is not empty: %s", strings.Join(nonEmpty, ", "))
	}
	return nil
}

// migrate applies pending migrations through the regular db layer.
func migrate(ctx context.Context, e endpoint, log *slog.Logger) error {
	d, err := db.Open(ctx, e.url)
	if err != nil {
		return err
	}
	defer d.Close()
	return db.Migrate(ctx, d, log)
}

// inspectVersion reads the schema version over a read-only connection.
func inspectVersion(ctx context.Context, e endpoint) (int64, error) {
	r, err := openReader(ctx, e)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	return schemaVersion(ctx, r.Query, e.dialect)
}

// schemaVersion returns goose's current version (0 without a version table).
func schemaVersion(ctx context.Context, q queryFunc, d db.Dialect) (int64, error) {
	probe := `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = '` + gooseTable + `'`
	if d == db.Postgres {
		probe = `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = current_schema() AND tablename = '` + gooseTable + `'`
	}
	rows, err := q(ctx, probe)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if toInt(rows[0][0]) == 0 {
		return 0, nil
	}
	rows, err = q(ctx, `SELECT coalesce(max(version_id), 0) FROM `+gooseTable)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return toInt(rows[0][0]), nil
}

// latestVersion is the newest migration version embedded in this binary.
func latestVersion(d db.Dialect) (int64, error) {
	dir := "postgres"
	if d == db.SQLite {
		dir = "sqlite"
	}
	entries, err := fs.ReadDir(migrations.FS, dir)
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		if !ok || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if v, err := strconv.ParseInt(num, 10, 64); err == nil && v > latest {
			latest = v
		}
	}
	return latest, nil
}

// reference is a freshly migrated schema: the table list for dry runs and the
// number of rows the migrations themselves insert per table (e.g. the
// billing.enforce setting, the default user group), which do not make a
// target "non-empty". Seeds are compared by count, not by key, so a migration
// that seeds generated ids still works.
type reference struct {
	tables []*table
	seeds  map[string]int64 // table → rows inserted by migrations
}

func loadReference(ctx context.Context) (*reference, error) {
	dir, err := os.MkdirTemp("", "omnigate-migrate-db-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "reference.db")
	e := endpoint{url: "sqlite://" + path, dialect: db.SQLite, path: path}
	if err := migrate(ctx, e, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		return nil, err
	}
	r, err := openReader(ctx, e)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	ref := &reference{seeds: map[string]int64{}}
	if ref.tables, err = describeSQLite(ctx, r.Query); err != nil {
		return nil, err
	}
	for _, t := range ref.tables {
		rows, err := r.Query(ctx, "SELECT count(*) FROM "+quoteIdent(t.Name))
		if err != nil {
			return nil, err
		}
		if n := toInt(rows[0][0]); n > 0 {
			ref.seeds[t.Name] = n
		}
	}
	return ref, nil
}

// nonEmptyTables lists target tables holding more rows than the migrations
// seed.
func nonEmptyTables(ctx context.Context, q queryFunc, tables []*table, seeds map[string]int64) ([]string, error) {
	var out []string
	for _, t := range tables {
		rows, err := q(ctx, "SELECT count(*) FROM "+quoteIdent(t.Name))
		if err != nil {
			return nil, fmt.Errorf("count %s: %w", t.Name, err)
		}
		if n := toInt(rows[0][0]); n > seeds[t.Name] {
			out = append(out, fmt.Sprintf("%s (%d rows)", t.Name, n))
		}
	}
	return out, nil
}

// monthlyPartitions are the partitioned PostgreSQL tables whose partitions
// OmniGate manages: function(month) creates the partition of that month (and
// the next).
var monthlyPartitions = map[string]func(ctx context.Context, d *db.DB, month time.Time) error{
	"request_logs": requestlog.EnsurePartitions,
}

// preparePartitions creates the monthly partitions covering the source rows of
// a partitioned target table, so rows land in their month's partition rather
// than the DEFAULT partition (which would block creating that month later).
func preparePartitions(ctx context.Context, dst endpoint, r reader, tp *tablePlan, out io.Writer, log *slog.Logger) error {
	ensure, known := monthlyPartitions[tp.Name]
	if !known || tp.PartKey == "" {
		fmt.Fprintf(out, "note: %s is partitioned; rows are written through the parent table (a matching or DEFAULT partition must exist)\n", tp.Name)
		return nil
	}
	col, ok := tp.column(tp.PartKey)
	if !ok {
		return fmt.Errorf("partition key %s.%s not found", tp.Name, tp.PartKey)
	}
	rows, err := r.Query(ctx, "SELECT min("+quoteIdent(col.Name)+"), max("+quoteIdent(col.Name)+") FROM "+quoteIdent(tp.Name))
	if err != nil {
		return fmt.Errorf("partition range of %s: %w", tp.Name, err)
	}
	if rows[0][0] == nil {
		return nil
	}
	lo, err := parseTime(rows[0][0])
	if err != nil {
		return err
	}
	hi, err := parseTime(rows[0][1])
	if err != nil {
		return err
	}
	d, err := db.Open(ctx, dst.url)
	if err != nil {
		return err
	}
	defer d.Close()
	n := 0
	for m := time.Date(lo.Year(), lo.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(hi); m = m.AddDate(0, 1, 0) {
		if err := ensure(ctx, d, m); err != nil {
			return err
		}
		n++
	}
	fmt.Fprintf(out, "ensured %d monthly partitions of %s (%s – %s)\n", n, tp.Name, lo.Format("2006-01"), hi.Format("2006-01"))
	log.Debug("partitions ensured", "table", tp.Name, "months", n)
	return nil
}
