// Package db is OmniGate's data-access layer (ADR-0009). Stores talk to the
// small interfaces defined here (Querier, Tx, Rows, Row, Result) and never to a
// driver directly, so the same store code runs on PostgreSQL (pgxpool) and on
// SQLite (modernc.org/sqlite through database/sql).
//
// SQL is written in PostgreSQL syntax with $N placeholders. On SQLite every
// statement is translated once (and cached): placeholders become ?N, casts
// (::type), row locks (FOR UPDATE / FOR SHARE) and advisory constructs are
// removed, now() becomes a UTC timestamp in the canonical text format, and
// "= ANY($N)" becomes "IN (SELECT value FROM json_each(?N))" with the slice
// argument passed as a JSON array. Constructs that cannot be translated
// mechanically go through the Dialect helpers.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoRows is returned by Row.Scan when the query selected no rows. Both
// drivers' errors match it with errors.Is (pgx.ErrNoRows wraps sql.ErrNoRows).
var ErrNoRows = sql.ErrNoRows

// Result is the outcome of Exec.
type Result interface {
	RowsAffected() int64
}

// Row is the result of QueryRow; Scan returns ErrNoRows when there is no row.
type Row interface {
	Scan(dest ...any) error
}

// Rows iterates over a query result. Close must be called (it is idempotent).
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// Querier is implemented by *DB and Tx so stores can run inside or outside a
// transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (Result, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Dialect() Dialect
}

// Tx is a database transaction. On SQLite every transaction is a write
// transaction (BEGIN IMMEDIATE), so row locks are implied.
type Tx interface {
	Querier
	// CopyFrom bulk-inserts rows (COPY on PostgreSQL, a prepared INSERT on SQLite).
	CopyFrom(ctx context.Context, table string, columns []string, rows [][]any) (int64, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// engine is the driver-specific part of a DB.
type engine interface {
	Querier
	Begin(ctx context.Context) (Tx, error)
	CopyFrom(ctx context.Context, table string, columns []string, rows [][]any) (int64, error)
	Ping(ctx context.Context) error
	Close()
}

// DB is a database handle (a connection pool).
type DB struct {
	engine
	pg *pgxpool.Pool // PostgreSQL only
	sq *sqliteDB     // SQLite only
}

// IsSQLiteURL reports whether url selects the SQLite backend.
func IsSQLiteURL(url string) bool { return strings.HasPrefix(url, "sqlite:") }

// Open connects to the database selected by url and verifies the connection:
// "sqlite:///abs/path.db", "sqlite://relative.db" or "sqlite::memory:" open
// SQLite (the file's directory is created when missing); anything else is a
// PostgreSQL connection string.
func Open(ctx context.Context, url string) (*DB, error) {
	if IsSQLiteURL(url) {
		sq, err := openSQLite(ctx, url)
		if err != nil {
			return nil, err
		}
		return &DB{engine: sq, sq: sq}, nil
	}
	pool, err := openPostgres(ctx, url)
	if err != nil {
		return nil, err
	}
	return &DB{engine: &pgEngine{pool: pool}, pg: pool}, nil
}

// PGPool returns the underlying pgx pool (nil on SQLite). Only PostgreSQL-
// specific maintenance code (partitions, advisory locks) may use it.
func (d *DB) PGPool() *pgxpool.Pool { return d.pg }

// SQLPath returns the SQLite database file path ("" on PostgreSQL or for an
// in-memory database).
func (d *DB) SQLPath() string {
	if d.sq == nil {
		return ""
	}
	return d.sq.path
}

// InTx runs fn in a transaction, committing on nil error and rolling back
// otherwise.
func InTx(ctx context.Context, d *DB, fn func(tx Tx) error) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IsNoRows reports whether err means no rows were returned.
func IsNoRows(err error) bool { return errors.Is(err, ErrNoRows) }

// IsUniqueViolation reports whether err is a unique-constraint (or primary
// key) violation.
func IsUniqueViolation(err error) bool { return pgCode(err) == "23505" || sqliteUnique(err) }

// IsForeignKeyViolation reports whether err is a foreign-key violation.
func IsForeignKeyViolation(err error) bool { return pgCode(err) == "23503" || sqliteForeignKey(err) }

// CollectRows scans single-column rows into a slice and closes rows.
func CollectRows[T any](rows Rows) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var v T
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// placeholders returns "$from, $from+1, …" (n of them).
func placeholders(from, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "$%d", from+i)
	}
	return b.String()
}
