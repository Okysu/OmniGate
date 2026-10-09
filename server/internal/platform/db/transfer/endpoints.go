package transfer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"omnigate/internal/platform/db"
)

// endpoint is a parsed --from / --to database URL.
type endpoint struct {
	url     string
	dialect db.Dialect
	path    string // SQLite file path
}

func parseEndpoint(raw string) (endpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return endpoint{}, errors.New("database url is empty")
	}
	if !db.IsSQLiteURL(raw) {
		if _, err := pgxpool.ParseConfig(raw); err != nil {
			return endpoint{}, fmt.Errorf("parse postgres url: %w", err)
		}
		return endpoint{url: raw, dialect: db.Postgres}, nil
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(raw, "sqlite:"), "//")
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" || rest == ":memory:" {
		return endpoint{}, fmt.Errorf("sqlite url %q must name a database file", raw)
	}
	return endpoint{url: raw, dialect: db.SQLite, path: rest}, nil
}

// String describes the endpoint without credentials.
func (e endpoint) String() string {
	if e.dialect == db.SQLite {
		return "sqlite " + e.path
	}
	cfg, err := pgxpool.ParseConfig(e.url)
	if err != nil {
		return "postgres"
	}
	return fmt.Sprintf("postgres %s:%d/%s", cfg.ConnConfig.Host, cfg.ConnConfig.Port, cfg.ConnConfig.Database)
}

// same reports whether two endpoints name the same database.
func (e endpoint) same(o endpoint) bool {
	if e.dialect != o.dialect {
		return false
	}
	if e.dialect == db.SQLite {
		a, err1 := filepath.Abs(e.path)
		b, err2 := filepath.Abs(o.path)
		return err1 == nil && err2 == nil && a == b
	}
	ca, err1 := pgxpool.ParseConfig(e.url)
	cb, err2 := pgxpool.ParseConfig(o.url)
	if err1 != nil || err2 != nil {
		return e.url == o.url
	}
	return ca.ConnConfig.Host == cb.ConnConfig.Host && ca.ConnConfig.Port == cb.ConnConfig.Port &&
		ca.ConnConfig.Database == cb.ConnConfig.Database
}

func (e endpoint) exists() bool {
	if e.dialect != db.SQLite {
		return true
	}
	_, err := os.Stat(e.path)
	return err == nil
}

// sqliteURI is a read-only SQLite URI for path (busy timeout, query_only).
func sqliteURI(path string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")
	return "file:" + r.Replace(path) + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
}

// reader reads one consistent snapshot of a database: a REPEATABLE READ,
// READ ONLY transaction on PostgreSQL (on a pool whose sessions default to
// read-only), a read transaction on a mode=ro connection on SQLite.
type reader interface {
	Dialect() db.Dialect
	Query(ctx context.Context, q string, args ...any) ([][]any, error)
	// Stream calls fn with the driver values of every row (the slice is reused).
	Stream(ctx context.Context, q string, fn func(row []any) error) error
	Close()
}

// writer writes the target inside one transaction.
type writer interface {
	Dialect() db.Dialect
	// Query and Exec run inside the transaction while one is open.
	Query(ctx context.Context, q string, args ...any) ([][]any, error)
	Begin(ctx context.Context) error
	Exec(ctx context.Context, q string, args ...any) error
	Write(ctx context.Context, table string, columns []string, rows [][]any) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context)
	Close()
}

func openReader(ctx context.Context, e endpoint) (reader, error) {
	if e.dialect == db.SQLite {
		if !e.exists() {
			return nil, fmt.Errorf("sqlite database %s does not exist", e.path)
		}
		sdb, err := sql.Open("sqlite", sqliteURI(e.path))
		if err != nil {
			return nil, err
		}
		sdb.SetMaxOpenConns(1)
		conn, err := sdb.Conn(ctx)
		if err != nil {
			sdb.Close()
			return nil, fmt.Errorf("open %s read-only: %w", e, err)
		}
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			conn.Close()
			sdb.Close()
			return nil, fmt.Errorf("open %s read-only: %w", e, err)
		}
		return &sqliteReader{sdb: sdb, conn: conn}, nil
	}
	cfg, err := pgxpool.ParseConfig(e.url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 2
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("open %s: %w", e, err)
	}
	return &pgReader{pool: pool, tx: tx}, nil
}

type sqliteReader struct {
	sdb  *sql.DB
	conn *sql.Conn
}

func (r *sqliteReader) Dialect() db.Dialect { return db.SQLite }

func (r *sqliteReader) Query(ctx context.Context, q string, args ...any) ([][]any, error) {
	return sqlQuery(ctx, r.conn, q, args...)
}

func (r *sqliteReader) Stream(ctx context.Context, q string, fn func([]any) error) error {
	return sqlStream(ctx, r.conn, q, fn)
}

func (r *sqliteReader) Close() {
	_, _ = r.conn.ExecContext(context.Background(), "ROLLBACK")
	r.conn.Close()
	r.sdb.Close()
}

type sqlConn interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func sqlQuery(ctx context.Context, c sqlConn, q string, args ...any) ([][]any, error) {
	var out [][]any
	err := sqlStream(ctx, c, q, func(row []any) error {
		out = append(out, append([]any(nil), row...))
		return nil
	}, args...)
	return out, err
}

func sqlStream(ctx context.Context, c sqlConn, q string, fn func([]any) error, args ...any) error {
	rows, err := c.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if err := fn(vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

type pgReader struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
}

func (r *pgReader) Dialect() db.Dialect { return db.Postgres }

func (r *pgReader) Query(ctx context.Context, q string, args ...any) ([][]any, error) {
	return pgQuery(ctx, r.tx, q, args...)
}

func (r *pgReader) Stream(ctx context.Context, q string, fn func([]any) error) error {
	return pgStream(ctx, r.tx, q, fn)
}

func (r *pgReader) Close() {
	_ = r.tx.Rollback(context.Background())
	r.pool.Close()
}

type pgQuerier interface {
	Query(ctx context.Context, q string, args ...any) (pgx.Rows, error)
}

func pgQuery(ctx context.Context, c pgQuerier, q string, args ...any) ([][]any, error) {
	var out [][]any
	err := pgStream(ctx, c, q, func(row []any) error {
		out = append(out, row)
		return nil
	}, args...)
	return out, err
}

func pgStream(ctx context.Context, c pgQuerier, q string, fn func([]any) error, args ...any) error {
	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return err
		}
		if err := fn(vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

func openWriter(ctx context.Context, e endpoint) (writer, error) {
	if e.dialect == db.SQLite {
		// Foreign keys are off on this connection while the copy runs (tables
		// are written parent-first anyway); PRAGMA foreign_key_check runs before
		// COMMIT.
		dsn := e.path + "?" + url.Values{"_pragma": {"busy_timeout(5000)", "foreign_keys(0)"}}.Encode()
		sdb, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		sdb.SetMaxOpenConns(1)
		conn, err := sdb.Conn(ctx)
		if err != nil {
			sdb.Close()
			return nil, fmt.Errorf("open %s: %w", e, err)
		}
		return &sqliteWriter{sdb: sdb, conn: conn}, nil
	}
	cfg, err := pgxpool.ParseConfig(e.url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("open %s: %w", e, err)
	}
	return &pgWriter{pool: pool}, nil
}

type sqliteWriter struct {
	sdb  *sql.DB
	conn *sql.Conn
	inTx bool
}

func (w *sqliteWriter) Dialect() db.Dialect { return db.SQLite }

func (w *sqliteWriter) Query(ctx context.Context, q string, args ...any) ([][]any, error) {
	return sqlQuery(ctx, w.conn, q, args...)
}

func (w *sqliteWriter) Begin(ctx context.Context) error {
	if _, err := w.conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	if _, err := w.conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	w.inTx = true
	return nil
}

func (w *sqliteWriter) Exec(ctx context.Context, q string, args ...any) error {
	_, err := w.conn.ExecContext(ctx, q, args...)
	return err
}

func (w *sqliteWriter) Write(ctx context.Context, table string, columns []string, rows [][]any) error {
	ph := make([]string, len(columns))
	for i := range ph {
		ph[i] = fmt.Sprintf("?%d", i+1)
	}
	st, err := w.conn.PrepareContext(ctx, "INSERT INTO "+quoteIdent(table)+" ("+quoteList(columns)+") VALUES ("+strings.Join(ph, ", ")+")")
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		if _, err := st.ExecContext(ctx, r...); err != nil {
			return err
		}
	}
	return nil
}

func (w *sqliteWriter) Commit(ctx context.Context) error {
	_, err := w.conn.ExecContext(ctx, "COMMIT")
	if err == nil {
		w.inTx = false
	}
	return err
}

func (w *sqliteWriter) Rollback(ctx context.Context) {
	if w.inTx {
		_, _ = w.conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		w.inTx = false
	}
}

func (w *sqliteWriter) Close() {
	w.Rollback(context.Background())
	w.conn.Close()
	w.sdb.Close()
}

type pgWriter struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
}

func (w *pgWriter) Dialect() db.Dialect { return db.Postgres }

func (w *pgWriter) Query(ctx context.Context, q string, args ...any) ([][]any, error) {
	if w.tx != nil {
		return pgQuery(ctx, w.tx, q, args...)
	}
	return pgQuery(ctx, w.pool, q, args...)
}

func (w *pgWriter) Begin(ctx context.Context) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	w.tx = tx
	return nil
}

func (w *pgWriter) Exec(ctx context.Context, q string, args ...any) error {
	if w.tx == nil {
		_, err := w.pool.Exec(ctx, q, args...)
		return err
	}
	_, err := w.tx.Exec(ctx, q, args...)
	return err
}

func (w *pgWriter) Write(ctx context.Context, table string, columns []string, rows [][]any) error {
	_, err := w.tx.CopyFrom(ctx, pgx.Identifier{table}, columns, pgx.CopyFromRows(rows))
	return err
}

func (w *pgWriter) Commit(ctx context.Context) error {
	err := w.tx.Commit(ctx)
	w.tx = nil
	return err
}

func (w *pgWriter) Rollback(ctx context.Context) {
	if w.tx != nil {
		_ = w.tx.Rollback(context.WithoutCancel(ctx))
		w.tx = nil
	}
}

func (w *pgWriter) Close() {
	w.Rollback(context.Background())
	w.pool.Close()
}
