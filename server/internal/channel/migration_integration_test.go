package channel_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
)

// TestShareAcceptanceMigration (00015, phase5-api.md §5.1): shares that
// existed before the upgrade stay effective (accepted); rolling back drops
// the shares that were never accepted, since the old schema treats every
// row as an effective share.
func TestShareAcceptanceMigration(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Open(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.MigrateDownTo(ctx, d, 14); err != nil {
		t.Fatalf("down to 14: %v", err)
	}
	now := time.Now().UTC()
	owner, a, b := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, a, b} {
		if _, err := d.Exec(ctx, `INSERT INTO users (id, display_name, role, status, created_at, updated_at) VALUES ($1, 'u', 'user', 'active', $2, $2)`,
			id, now); err != nil {
			t.Fatal(err)
		}
	}
	ch := uuid.New()
	if _, err := d.Exec(ctx, `INSERT INTO channels (id, owner_id, name, type, base_url, scope, status, created_at, updated_at)
		VALUES ($1, $2, 'c', 'openai', 'https://x.test', 'shared', 'enabled', $3, $3)`, ch, owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, `INSERT INTO channel_shares (channel_id, user_id, created_at) VALUES ($1, $2, $3)`, ch, a, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, log); err != nil {
		t.Fatal(err)
	}
	var status string
	var responded *time.Time
	if err := d.QueryRow(ctx, `SELECT status, responded_at FROM channel_shares WHERE channel_id = $1 AND user_id = $2`, ch, a).
		Scan(&status, &responded); err != nil || status != "accepted" || responded == nil {
		t.Fatalf("existing share = %s %v %v", status, responded, err)
	}
	// New rows default to pending; unknown statuses are rejected.
	if _, err := d.Exec(ctx, `INSERT INTO channel_shares (channel_id, user_id) VALUES ($1, $2)`, ch, b); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(ctx, `SELECT status FROM channel_shares WHERE channel_id = $1 AND user_id = $2`, ch, b).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("default status = %s %v", status, err)
	}
	if _, err := d.Exec(ctx, `UPDATE channel_shares SET status = 'bogus' WHERE user_id = $1`, b); err == nil {
		t.Fatal("bogus status accepted")
	}
	if err := db.MigrateDownTo(ctx, d, 14); err != nil {
		t.Fatalf("down to 14 again: %v", err)
	}
	var n int
	if err := d.QueryRow(ctx, `SELECT count(*) FROM channel_shares WHERE channel_id = $1`, ch).Scan(&n); err != nil || n != 1 {
		t.Fatalf("shares after down = %d %v (the pending one must be gone)", n, err)
	}
	if err := db.Migrate(ctx, d, log); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
