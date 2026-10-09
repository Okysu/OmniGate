package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// SQLite runtime parameters (ADR-0009 §6).
const (
	sqliteBusyTimeout = 5 * time.Second
	// sqliteWriteWait bounds how long a writer queues for the in-process write
	// lock before failing (a write issued on the pool from inside a transaction
	// callback would otherwise wait forever).
	sqliteWriteWait = 30 * time.Second
)

var memSeq atomic.Int64

// sqliteDB is the SQLite engine. SQLite allows one writer at a time; writers
// are queued on an in-process semaphore before they take a connection, so a
// transaction never waits for a pool slot held by a blocked writer and readers
// (WAL) keep running concurrently.
type sqliteDB struct {
	db         *sql.DB
	path       string        // file path ("" for in-memory)
	migrateDSN string        // DSN for the migration connection (foreign keys off)
	write      chan struct{} // write lock: capacity 1
}

// parseSQLiteURL maps sqlite:///abs.db, sqlite://rel.db, sqlite:rel.db and
// sqlite::memory: to a file path ("" = in-memory) and extra DSN parameters.
func parseSQLiteURL(raw string) (path string, params url.Values, err error) {
	rest := strings.TrimPrefix(raw, "sqlite:")
	rest = strings.TrimPrefix(rest, "//")
	query := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, query = rest[:i], rest[i+1:]
	}
	params, err = url.ParseQuery(query)
	if err != nil {
		return "", nil, fmt.Errorf("parse sqlite url parameters: %w", err)
	}
	if rest == ":memory:" {
		return "", params, nil
	}
	if rest == "" {
		return "", nil, errors.New("sqlite url needs a file path, e.g. sqlite:///data/omnigate.db")
	}
	return rest, params, nil
}

// sqliteDSN builds the driver DSN. Foreign keys are enforced except on the
// migration connection (table rebuilds need them off; Migrate checks them
// afterwards).
func sqliteDSN(name string, memory bool, extra url.Values, foreignKeys bool) string {
	q := url.Values{}
	q.Set("_txlock", "immediate")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeout.Milliseconds()))
	if foreignKeys {
		q.Add("_pragma", "foreign_keys(1)")
	} else {
		q.Add("_pragma", "foreign_keys(0)")
	}
	if !memory {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	q.Add("_pragma", "synchronous(NORMAL)")
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	if memory {
		q.Set("vfs", "memdb") // one in-memory database shared by all pool connections
	}
	return name + "?" + q.Encode()
}

func openSQLite(ctx context.Context, raw string) (*sqliteDB, error) {
	registerFunctions()
	path, extra, err := parseSQLiteURL(raw)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if dir := filepath.Dir(path); dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("create sqlite directory: %w", err)
			}
		}
	}
	name := path
	if path == "" {
		name = fmt.Sprintf("file:/omnigate-mem-%d-%d", os.Getpid(), memSeq.Add(1))
	}
	dsn := sqliteDSN(name, path == "", extra, true)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	conns := max(4, runtime.GOMAXPROCS(0))
	sdb.SetMaxOpenConns(conns)
	sdb.SetMaxIdleConns(conns)
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("open sqlite database %q: %w", path, err)
	}
	if path != "" {
		var mode string
		if err := sdb.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			sdb.Close()
			return nil, err
		}
		if !strings.EqualFold(mode, "wal") {
			sdb.Close()
			return nil, fmt.Errorf("sqlite: could not enable WAL mode (journal_mode=%s)", mode)
		}
	}
	return &sqliteDB{db: sdb, path: path, migrateDSN: sqliteDSN(name, path == "", extra, false), write: make(chan struct{}, 1)}, nil
}

func (s *sqliteDB) Dialect() Dialect { return SQLite }

// lockWrite takes the in-process write lock.
func (s *sqliteDB) lockWrite(ctx context.Context) (func(), error) {
	t := time.NewTimer(sqliteWriteWait)
	defer t.Stop()
	select {
	case s.write <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-s.write }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, errors.New("sqlite: timed out waiting for the database write lock")
	}
}

// stmt is a translated statement.
type stmt struct {
	sql   string
	write bool
}

var stmtCache sync.Map // original SQL -> stmt

func prepareSQL(q string) stmt {
	if v, ok := stmtCache.Load(q); ok {
		return v.(stmt)
	}
	st := stmt{sql: translateSQLite(q), write: isWrite(q)}
	stmtCache.Store(q, st)
	return st
}

// isWrite reports whether a statement modifies the database.
func isWrite(q string) bool {
	code, _ := maskLiterals(q)
	f := strings.FieldsFunc(strings.ToUpper(code), func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r == '_')
	})
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "DROP", "ALTER", "VACUUM", "REINDEX":
		return true
	case "WITH":
		for _, w := range f[1:] {
			if w == "INSERT" || w == "UPDATE" || w == "DELETE" {
				return true
			}
		}
	}
	return false
}

func (s *sqliteDB) Exec(ctx context.Context, q string, args ...any) (Result, error) {
	st := prepareSQL(q)
	vals, err := sqliteArgs(args)
	if err != nil {
		return nil, err
	}
	if st.write {
		release, err := s.lockWrite(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	res, err := s.db.ExecContext(ctx, st.sql, vals...)
	if err != nil {
		return nil, err
	}
	return sqlResult{res}, nil
}

func (s *sqliteDB) Query(ctx context.Context, q string, args ...any) (Rows, error) {
	st := prepareSQL(q)
	vals, err := sqliteArgs(args)
	if err != nil {
		return nil, err
	}
	var release func()
	if st.write {
		if release, err = s.lockWrite(ctx); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, st.sql, vals...)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, err
	}
	return &sqlRows{rows: rows, release: release}, nil
}

func (s *sqliteDB) QueryRow(ctx context.Context, q string, args ...any) Row {
	rows, err := s.Query(ctx, q, args...)
	return &sqlRow{rows: rows, err: err}
}

func (s *sqliteDB) Begin(ctx context.Context) (Tx, error) {
	release, err := s.lockWrite(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (_txlock=immediate)
	if err != nil {
		release()
		return nil, err
	}
	return &sqliteTx{tx: tx, release: release}, nil
}

func (s *sqliteDB) CopyFrom(ctx context.Context, table string, columns []string, rows [][]any) (int64, error) {
	var n int64
	err := InTx(ctx, &DB{engine: s, sq: s}, func(tx Tx) error {
		var err error
		n, err = tx.CopyFrom(ctx, table, columns, rows)
		return err
	})
	return n, err
}

func (s *sqliteDB) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *sqliteDB) Close()                         { s.db.Close() }

// sqliteTx is a write transaction holding the write lock until it ends.
type sqliteTx struct {
	tx      *sql.Tx
	release func()
}

func (t *sqliteTx) Dialect() Dialect { return SQLite }

func (t *sqliteTx) Exec(ctx context.Context, q string, args ...any) (Result, error) {
	vals, err := sqliteArgs(args)
	if err != nil {
		return nil, err
	}
	res, err := t.tx.ExecContext(ctx, prepareSQL(q).sql, vals...)
	if err != nil {
		return nil, err
	}
	return sqlResult{res}, nil
}

func (t *sqliteTx) Query(ctx context.Context, q string, args ...any) (Rows, error) {
	vals, err := sqliteArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, prepareSQL(q).sql, vals...)
	if err != nil {
		return nil, err
	}
	return &sqlRows{rows: rows}, nil
}

func (t *sqliteTx) QueryRow(ctx context.Context, q string, args ...any) Row {
	rows, err := t.Query(ctx, q, args...)
	return &sqlRow{rows: rows, err: err}
}

func (t *sqliteTx) CopyFrom(ctx context.Context, table string, columns []string, rows [][]any) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	ins := `INSERT INTO "` + strings.ReplaceAll(table, `"`, `""`) + `" (` + strings.Join(quoted, ", ") +
		`) VALUES (` + placeholders(1, len(columns)) + `)`
	ps, err := t.tx.PrepareContext(ctx, translateSQLite(ins))
	if err != nil {
		return 0, err
	}
	defer ps.Close()
	for _, r := range rows {
		vals, err := sqliteArgs(r)
		if err != nil {
			return 0, err
		}
		if _, err := ps.ExecContext(ctx, vals...); err != nil {
			return 0, err
		}
	}
	return int64(len(rows)), nil
}

func (t *sqliteTx) Commit(context.Context) error {
	defer t.release()
	return t.tx.Commit()
}

func (t *sqliteTx) Rollback(context.Context) error {
	defer t.release()
	err := t.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

type sqlResult struct{ res sql.Result }

func (r sqlResult) RowsAffected() int64 {
	n, _ := r.res.RowsAffected()
	return n
}

// sqlRows adapts *sql.Rows: Scan converts SQLite storage values the way pgx
// scans PostgreSQL types (see assign).
type sqlRows struct {
	rows    *sql.Rows
	raw     []any
	ptrs    []any
	release func()
	closed  bool
	err     error
}

func (r *sqlRows) Next() bool {
	if r.closed {
		return false
	}
	if r.rows.Next() {
		return true
	}
	r.Close()
	return false
}

func (r *sqlRows) Scan(dest ...any) error {
	if r.raw == nil {
		cols, err := r.rows.Columns()
		if err != nil {
			return err
		}
		r.raw = make([]any, len(cols))
		r.ptrs = make([]any, len(cols))
		for i := range r.raw {
			r.ptrs[i] = &r.raw[i]
		}
	}
	if len(dest) != len(r.raw) {
		return fmt.Errorf("db: scan expected %d destinations, got %d", len(r.raw), len(dest))
	}
	if err := r.rows.Scan(r.ptrs...); err != nil {
		return err
	}
	for i, d := range dest {
		if err := assign(d, r.raw[i]); err != nil {
			return fmt.Errorf("db: scan column %d: %w", i, err)
		}
	}
	return nil
}

func (r *sqlRows) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.rows.Err()
}

func (r *sqlRows) Close() {
	if r.closed {
		return
	}
	r.closed = true
	if err := r.rows.Close(); err != nil && r.err == nil {
		r.err = err
	}
	if r.release != nil {
		r.release()
	}
}

type sqlRow struct {
	rows Rows
	err  error
}

func (r *sqlRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return ErrNoRows
	}
	if err := r.rows.Scan(dest...); err != nil {
		return err
	}
	r.rows.Close()
	return r.rows.Err()
}

func sqliteCode(err error) int {
	var e *sqlite.Error
	if errors.As(err, &e) {
		return e.Code()
	}
	return 0
}

func sqliteUnique(err error) bool {
	c := sqliteCode(err)
	return c == sqlite3.SQLITE_CONSTRAINT_UNIQUE || c == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

func sqliteForeignKey(err error) bool { return sqliteCode(err) == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY }
