package limits

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

// Increment adds requests and charge to one counter.
type Increment struct {
	Scope    string
	ScopeID  uuid.UUID
	Window   Window
	Start    time.Time
	Requests int64
	Charge   money.Amount
}

// Counter is a counter value after an increment.
type Counter struct {
	Scope    string
	ScopeID  uuid.UUID
	Window   Window
	Start    time.Time
	Requests int64
	Charge   money.Amount
}

// Apply adds incs to their counters with one upsert in q (the settlement
// transaction) and returns the new values. Rows are written in a fixed order
// so concurrent settlements never deadlock.
func Apply(ctx context.Context, q db.Querier, incs []Increment, now time.Time) ([]Counter, error) {
	if len(incs) == 0 {
		return nil, nil
	}
	incs = append([]Increment(nil), incs...)
	sort.Slice(incs, func(i, j int) bool {
		a, b := incs[i], incs[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.ScopeID != b.ScopeID {
			return a.ScopeID.String() < b.ScopeID.String()
		}
		if a.Window != b.Window {
			return a.Window < b.Window
		}
		return a.Start.Before(b.Start)
	})
	var sb strings.Builder
	args := make([]any, 0, len(incs)*6+1)
	args = append(args, now)
	sb.WriteString(`INSERT INTO usage_counters (scope, scope_id, window_kind, window_start, requests, charge_nano, updated_at) VALUES `)
	for i, in := range incs {
		if i > 0 {
			sb.WriteString(", ")
		}
		n := len(args)
		sb.WriteString("(")
		for j := 1; j <= 6; j++ {
			sb.WriteString("$" + strconv.Itoa(n+j) + ", ")
		}
		sb.WriteString("$1)")
		args = append(args, in.Scope, in.ScopeID, string(in.Window), in.Start.UTC(), in.Requests, int64(in.Charge))
	}
	sb.WriteString(` ON CONFLICT (scope, scope_id, window_kind, window_start) DO UPDATE SET
		requests = usage_counters.requests + EXCLUDED.requests,
		charge_nano = usage_counters.charge_nano + EXCLUDED.charge_nano,
		updated_at = EXCLUDED.updated_at
		RETURNING scope, scope_id, window_kind, window_start, requests, charge_nano`)
	rows, err := q.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Counter, 0, len(incs))
	for rows.Next() {
		var c Counter
		var w string
		var charge int64
		if err := rows.Scan(&c.Scope, &c.ScopeID, &w, &c.Start, &c.Requests, &charge); err != nil {
			return nil, err
		}
		c.Window, c.Charge, c.Start = Window(w), money.Amount(charge), c.Start.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// read returns the counter (zero when it does not exist).
func read(ctx context.Context, q db.Querier, scope string, id uuid.UUID, w Window, start time.Time) (int64, money.Amount, error) {
	var reqs, charge int64
	err := q.QueryRow(ctx, `SELECT requests, charge_nano FROM usage_counters
		WHERE scope = $1 AND scope_id = $2 AND window_kind = $3 AND window_start = $4`, scope, id, string(w), start.UTC()).Scan(&reqs, &charge)
	if db.IsNoRows(err) {
		return 0, 0, nil
	}
	return reqs, money.Amount(charge), err
}

// Prune deletes counters of windows that ended long ago (days and weeks
// after 60 days, months after 400 days) and returns how many were deleted.
func Prune(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM usage_counters WHERE (window_kind IN ('day', 'week') AND window_start < $1)
		OR (window_kind = 'month' AND window_start < $2)`, now.AddDate(0, 0, -60), now.AddDate(0, 0, -400))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
