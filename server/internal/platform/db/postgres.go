package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// pgEngine wraps pgxpool; pgx rows, rows and command tags already satisfy
// the package interfaces, so behaviour is exactly pgx's.
type pgEngine struct{ pool *pgxpool.Pool }

func (e *pgEngine) Dialect() Dialect { return Postgres }

func (e *pgEngine) Exec(ctx context.Context, sql string, args ...any) (Result, error) {
	return e.pool.Exec(ctx, sql, args...)
}

func (e *pgEngine) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return e.pool.Query(ctx, sql, args...)
}

func (e *pgEngine) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return e.pool.QueryRow(ctx, sql, args...)
}

func (e *pgEngine) Begin(ctx context.Context) (Tx, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgTx{tx}, nil
}

func (e *pgEngine) CopyFrom(ctx context.Context, table string, columns []string, rows [][]any) (int64, error) {
	return e.pool.CopyFrom(ctx, pgx.Identifier{table}, columns, pgx.CopyFromRows(rows))
}

func (e *pgEngine) Ping(ctx context.Context) error { return e.pool.Ping(ctx) }
func (e *pgEngine) Close()                         { e.pool.Close() }

type pgTx struct{ tx pgx.Tx }

func (t pgTx) Dialect() Dialect { return Postgres }

func (t pgTx) Exec(ctx context.Context, sql string, args ...any) (Result, error) {
	return t.tx.Exec(ctx, sql, args...)
}

func (t pgTx) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return t.tx.Query(ctx, sql, args...)
}

func (t pgTx) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return t.tx.QueryRow(ctx, sql, args...)
}

func (t pgTx) CopyFrom(ctx context.Context, table string, columns []string, rows [][]any) (int64, error) {
	return t.tx.CopyFrom(ctx, pgx.Identifier{table}, columns, pgx.CopyFromRows(rows))
}

func (t pgTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t pgTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
