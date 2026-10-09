package usergroup_test

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

const defaultGroupID = "01920000-0000-7000-8000-000000000001"

// TestMigrationFromV11 upgrades a populated v11 database (users, a shared
// channel, prices, request logs) to v12 on the configured dialect: the
// default group "默认" holds every existing user, new rows without a group
// join the default group, and existing data is untouched. Down to 11 and up
// again keeps working.
func TestMigrationFromV11(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Open(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.MigrateDownTo(ctx, d, 11); err != nil {
		t.Fatalf("down to 11: %v", err)
	}
	if st, err := db.MigrationStatus(ctx, d); err != nil || st.Current != 11 {
		t.Fatalf("status %+v %v", st, err)
	}
	now := time.Now().UTC()
	users := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, id := range users {
		role := "user"
		if i == 0 {
			role = "system_admin"
		}
		if _, err := d.Exec(ctx, `INSERT INTO users (id, display_name, role, status, created_at, updated_at, disabled_reason)
			VALUES ($1, $2, $3, $4, $5, $5, $6)`, id, "u"+id.String()[:4], role, []string{"active", "active", "disabled"}[i], now,
			[]any{nil, nil, "spam"}[i]); err != nil {
			t.Fatal(err)
		}
	}
	ch := uuid.New()
	if _, err := d.Exec(ctx, `INSERT INTO channels (id, owner_id, name, type, base_url, scope, status, created_at, updated_at)
		VALUES ($1, $2, 'c', 'openai', 'https://x.test', 'shared', 'enabled', $3, $3)`, ch, users[1], now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, `INSERT INTO channel_shares (channel_id, user_id) VALUES ($1, $2)`, ch, users[2]); err != nil {
		t.Fatal(err)
	}
	price := uuid.New()
	if _, err := d.Exec(ctx, `INSERT INTO prices (id, kind, model, input_per_m, output_per_m, cache_read_per_m, cache_write_per_m,
		per_request, effective_at, created_at) VALUES ($1, 'sell', 'm', 1000, 2000, 0, 0, 0, $2, $2)`, price, now); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(ctx, d, log); err != nil {
		t.Fatalf("up to 12: %v", err)
	}
	var name string
	var isDefault bool
	var mult int64
	var tz string
	if err := d.QueryRow(ctx, `SELECT name, is_default, price_multiplier_nano, timezone FROM user_groups WHERE id = $1`, defaultGroupID).
		Scan(&name, &isDefault, &mult, &tz); err != nil {
		t.Fatal(err)
	}
	if name != "默认" || !isDefault || mult != 1_000_000_000 || tz != "Asia/Shanghai" {
		t.Fatalf("default group = %s %v %d %s", name, isDefault, mult, tz)
	}
	var groups, members, unlimited int
	if err := d.QueryRow(ctx, `SELECT count(*) FROM user_groups`).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(ctx, `SELECT count(*) FROM users WHERE group_id = $1`, defaultGroupID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(ctx, `SELECT count(*) FROM user_groups WHERE rpm IS NULL AND rpd IS NULL AND daily_spend_nano IS NULL
		AND monthly_spend_nano IS NULL`).Scan(&unlimited); err != nil {
		t.Fatal(err)
	}
	if groups != 1 || members != 3 || unlimited != 1 {
		t.Fatalf("groups %d members %d unlimited %d", groups, members, unlimited)
	}
	// Existing rows are untouched; new columns have their defaults.
	var reason *string
	if err := d.QueryRow(ctx, `SELECT disabled_reason FROM users WHERE id = $1`, users[2]).Scan(&reason); err != nil || reason == nil || *reason != "spam" {
		t.Fatalf("disabled reason = %v %v", reason, err)
	}
	var sched []byte
	var schedTZ string
	if err := d.QueryRow(ctx, `SELECT schedule, schedule_timezone FROM prices WHERE id = $1`, price).Scan(&sched, &schedTZ); err != nil ||
		sched != nil || schedTZ != "Asia/Shanghai" {
		t.Fatalf("price schedule = %q %q %v", sched, schedTZ, err)
	}
	var shares int
	if err := d.QueryRow(ctx, `SELECT count(*) FROM channel_shares WHERE channel_id = $1`, ch).Scan(&shares); err != nil || shares != 1 {
		t.Fatalf("shares = %d %v", shares, err)
	}

	// Users inserted without a group (other tools) join the current default.
	other := uuid.New()
	if _, err := d.Exec(ctx, `INSERT INTO user_groups (id, name) VALUES ($1, 'other')`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, `UPDATE user_groups SET is_default = (id = $1)`, other); err != nil {
		t.Fatal(err)
	}
	late := uuid.New()
	if _, err := d.Exec(ctx, `INSERT INTO users (id, display_name) VALUES ($1, 'late')`, late); err != nil {
		t.Fatal(err)
	}
	var gid uuid.UUID
	if err := d.QueryRow(ctx, `SELECT group_id FROM users WHERE id = $1`, late).Scan(&gid); err != nil || gid != other {
		t.Fatalf("late user group = %v %v", gid, err)
	}
	// An explicit group is kept.
	explicit := uuid.New()
	if _, err := d.Exec(ctx, `INSERT INTO users (id, display_name, group_id) VALUES ($1, 'x', $2)`, explicit, defaultGroupID); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(ctx, `SELECT group_id FROM users WHERE id = $1`, explicit).Scan(&gid); err != nil || gid.String() != defaultGroupID {
		t.Fatalf("explicit group = %v %v", gid, err)
	}
	// Only one default group.
	if _, err := d.Exec(ctx, `UPDATE user_groups SET is_default = true`); err == nil || !db.IsUniqueViolation(err) {
		t.Fatalf("two default groups: %v", err)
	}

	// Down keeps the users (and their other columns); up again recreates the default group.
	if err := db.MigrateDownTo(ctx, d, 11); err != nil {
		t.Fatalf("down again: %v", err)
	}
	var n int
	if err := d.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("users after down = %d %v", n, err)
	}
	if err := d.QueryRow(ctx, `SELECT disabled_reason FROM users WHERE id = $1`, users[2]).Scan(&reason); err != nil || *reason != "spam" {
		t.Fatalf("disabled reason after down = %v %v", reason, err)
	}
	if err := db.Migrate(ctx, d, log); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if err := d.QueryRow(ctx, `SELECT count(*) FROM users WHERE group_id = $1`, defaultGroupID).Scan(&n); err != nil || n != 5 {
		t.Fatalf("members after re-upgrade = %d %v", n, err)
	}
}
