// Package dbtest provides isolated databases for integration tests.
//
// OMNIGATE_TEST_DATABASE_URL selects the backend:
//
//   - postgres://… : a server where the user may CREATE DATABASE; every test
//     gets its own database, dropped afterwards;
//   - sqlite (or any sqlite: URL): every test gets a fresh database file in
//     its temporary directory.
//
// Tests skip when the variable is unset.
package dbtest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"omnigate/internal/platform/db"
)

var seq atomic.Int64

// Env is the environment variable naming the test database server.
const Env = "OMNIGATE_TEST_DATABASE_URL"

// URL returns the URL of a new, empty database for t (skipping t when no test
// database is configured).
func URL(t testing.TB) string {
	t.Helper()
	admin := strings.TrimSpace(os.Getenv(Env))
	if admin == "" {
		t.Skip(Env + " not set; skipping integration test")
	}
	if admin == "sqlite" || db.IsSQLiteURL(admin) {
		return "sqlite://" + filepath.Join(t.TempDir(), "omnigate.db")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect admin db: %v", err)
	}
	name := fmt.Sprintf("omnigate_test_%d_%d", time.Now().UnixNano(), seq.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		conn.Close(context.Background())
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Open returns a migrated database for t, closed when the test ends.
func Open(t testing.TB) *db.DB {
	t.Helper()
	return OpenURL(t, URL(t))
}

// OpenURL opens and migrates the database at u, closing it when t ends.
func OpenURL(t testing.TB, u string) *db.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := db.Migrate(ctx, d, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}
