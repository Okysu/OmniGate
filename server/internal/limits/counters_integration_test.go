package limits_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/limits"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
)

// TestApplyConcurrent increments the same counters from many transactions at
// once: no update is lost and the returned values are the running totals.
func TestApplyConcurrent(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Open(t)
	user, key := uuid.New(), uuid.New()
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	incs := func() []limits.Increment {
		return []limits.Increment{
			{Scope: limits.ScopeUser, ScopeID: user, Window: limits.Day, Start: limits.Start(limits.Day, at, time.UTC), Requests: 1, Charge: 7},
			{Scope: limits.ScopeKey, ScopeID: key, Window: limits.Total, Start: limits.Start(limits.Total, at, time.UTC), Charge: 7},
			{Scope: limits.ScopeUser, ScopeID: user, Window: limits.Month, Start: limits.Start(limits.Month, at, time.UTC), Requests: 1, Charge: 7},
		}
	}
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	seen := make(chan int64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.InTx(ctx, d, func(tx db.Tx) error {
				out, err := limits.Apply(ctx, tx, incs(), time.Now())
				for _, c := range out {
					if c.Scope == limits.ScopeUser && c.Window == limits.Day {
						seen <- c.Requests
					}
				}
				return err
			})
		}()
	}
	wg.Wait()
	close(errs)
	close(seen)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	values := map[int64]bool{}
	for v := range seen {
		values[v] = true
	}
	if len(values) != n || !values[1] || !values[n] {
		t.Fatalf("returned running totals = %v", values)
	}
	var reqs, charge int64
	if err := d.QueryRow(ctx, `SELECT requests, charge_nano FROM usage_counters WHERE scope = 'user' AND window_kind = 'day'`).Scan(&reqs, &charge); err != nil {
		t.Fatal(err)
	}
	if reqs != n || money.Amount(charge) != 7*n {
		t.Fatalf("day counter = %d %d", reqs, charge)
	}
	if err := d.QueryRow(ctx, `SELECT requests, charge_nano FROM usage_counters WHERE scope = 'key'`).Scan(&reqs, &charge); err != nil {
		t.Fatal(err)
	}
	if reqs != 0 || charge != 7*n {
		t.Fatalf("key counter = %d %d", reqs, charge)
	}
	// Pruning keeps current windows.
	if deleted, err := limits.Prune(ctx, d, at); err != nil || deleted != 0 {
		t.Fatalf("prune now = %d %v", deleted, err)
	}
	if deleted, err := limits.Prune(ctx, d, at.AddDate(1, 2, 0)); err != nil || deleted != 2 {
		t.Fatalf("prune later = %d %v", deleted, err)
	}
}
