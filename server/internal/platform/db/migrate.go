package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"omnigate/migrations"
)

// migrationFS returns the dialect's migration directory. Version numbers are
// identical in both directories; goose records only the version, so moving the
// PostgreSQL files into migrations/postgres changed nothing for existing
// databases.
func migrationFS(d Dialect) (fs.FS, goose.Dialect, error) {
	if d == SQLite {
		sub, err := fs.Sub(migrations.FS, "sqlite")
		return sub, goose.DialectSQLite3, err
	}
	sub, err := fs.Sub(migrations.FS, "postgres")
	return sub, goose.DialectPostgres, err
}

// provider returns a goose provider over sqlDB; close is called when done.
func (d *DB) provider(migrating bool) (p *goose.Provider, closeFn func(), err error) {
	fsys, dialect, err := migrationFS(d.Dialect())
	if err != nil {
		return nil, nil, err
	}
	var sqlDB *sql.DB
	closeFn = func() {}
	switch {
	case d.sq == nil:
		sqlDB = stdlib.OpenDBFromPool(d.pg)
		closeFn = func() { sqlDB.Close() }
	case migrating:
		// A dedicated connection with foreign keys off, as SQLite requires for
		// table rebuilds (CHECK changes); Migrate runs foreign_key_check after.
		if sqlDB, err = sql.Open("sqlite", d.sq.migrateDSN); err != nil {
			return nil, nil, err
		}
		sqlDB.SetMaxOpenConns(1)
		closeFn = func() { sqlDB.Close() }
	default:
		sqlDB = d.sq.db
	}
	p, err = goose.NewProvider(dialect, sqlDB, fsys)
	if err != nil {
		closeFn()
		return nil, nil, err
	}
	return p, closeFn, nil
}

// ErrSchemaTooNew means the database was migrated by a newer OmniGate
// version than this binary knows. Running against it could corrupt data
// (e.g. after rolling back the binary without restoring the backup).
var ErrSchemaTooNew = errors.New("database schema is newer than this OmniGate binary")

// CheckSchemaVersion fails with ErrSchemaTooNew when the database is ahead of
// the migrations embedded in this binary.
func CheckSchemaVersion(ctx context.Context, d *DB) error {
	st, err := MigrationStatus(ctx, d)
	if err != nil {
		return err
	}
	if st.TooNew {
		return fmt.Errorf("%w: database is at version %d, this binary supports up to %d; "+
			"upgrade OmniGate or restore the backup taken before the upgrade", ErrSchemaTooNew, st.Current, st.Latest)
	}
	return nil
}

// Migrate applies all pending migrations. Concurrent PostgreSQL instances are
// serialized by an advisory lock; on SQLite (single instance) the in-process
// write lock keeps other writers out while the schema changes.
func Migrate(ctx context.Context, d *DB, log *slog.Logger) error {
	if d.sq != nil {
		release, err := d.sq.lockWrite(ctx)
		if err != nil {
			return err
		}
		defer release()
	} else {
		conn, err := d.pg.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()
		const lockID = 0x6f6d6e69 // "omni"
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
			return err
		}
		defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockID) //nolint:errcheck
	}
	if err := CheckSchemaVersion(ctx, d); err != nil {
		return err
	}
	p, closeFn, err := d.provider(true)
	if err != nil {
		return err
	}
	defer closeFn()
	results, err := p.Up(ctx)
	for _, r := range results {
		log.Info("migration applied", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration,
			"dialect", d.Dialect().String())
	}
	if err != nil {
		return err
	}
	if d.sq != nil && len(results) > 0 {
		return sqliteForeignKeyCheck(ctx, d.sq.db)
	}
	return nil
}

// sqliteForeignKeyCheck fails when any row violates a foreign key.
func sqliteForeignKeyCheck(ctx context.Context, sqlDB *sql.DB) error {
	rows, err := sqlDB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		return fmt.Errorf("migrate: foreign key violation after migration: %s row %d references %s", table, rowid.Int64, parent)
	}
	return rows.Err()
}

// MigrateDownTo rolls migrations back to version (development and tests).
func MigrateDownTo(ctx context.Context, d *DB, version int64) error {
	p, closeFn, err := d.provider(true)
	if err != nil {
		return err
	}
	defer closeFn()
	_, err = p.DownTo(ctx, version)
	return err
}

// Status describes the schema version for /readyz and the CLI.
type Status struct {
	Dialect string `json:"dialect"`
	Current int64  `json:"current"`
	Latest  int64  `json:"latest"`
	Pending int    `json:"pending"`
	// TooNew: the database is ahead of this binary (see ErrSchemaTooNew).
	TooNew bool `json:"tooNew,omitempty"`
}

// MigrationStatus reports the applied and available schema versions.
func MigrationStatus(ctx context.Context, d *DB) (Status, error) {
	p, closeFn, err := d.provider(false)
	if err != nil {
		return Status{}, err
	}
	defer closeFn()
	cur, err := p.GetDBVersion(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{Dialect: d.Dialect().String(), Current: cur}
	for _, s := range p.ListSources() {
		if s.Version > st.Latest {
			st.Latest = s.Version
		}
		if s.Version > cur {
			st.Pending++
		}
	}
	st.TooNew = cur > st.Latest
	return st, nil
}

// Backup writes a consistent copy of a SQLite database to path with VACUUM
// INTO (safe while the server is running; path must not exist). PostgreSQL
// databases are backed up with pg_dump instead.
func Backup(ctx context.Context, d *DB, path string) error {
	if d.sq == nil {
		return fmt.Errorf("backup: only for SQLite; back up PostgreSQL with pg_dump")
	}
	if d.sq.path == "" {
		return fmt.Errorf("backup: in-memory database")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("backup: %s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	_, err := d.sq.db.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}
