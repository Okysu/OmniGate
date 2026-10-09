package subscription

import (
	"context"
	"time"
)

// Sweep deletes quota counters that can no longer affect any decision:
// all rows of subscriptions that ended or were cancelled more than 30 days
// ago, rolling/session rows older than 32 days (windows are at most 31 days),
// calendar/period rows older than 400 days, and idempotency markers older
// than 90 days (request logs keep subscription_id/quota_charge for auditing).
// Lifetime counters of live subscriptions are never touched.
func (s *Service) Sweep(ctx context.Context, now time.Time) (int64, error) {
	now = now.UTC()
	elems := s.pool.Dialect().JSONArrayElements("s.rules", "r")
	var total int64
	for _, q := range []struct {
		sql    string
		cutoff time.Time
	}{
		{`DELETE FROM quota_usage WHERE subscription_id IN (SELECT id FROM subscriptions
			WHERE ends_at < $1 OR (status = 'cancelled' AND cancelled_at < $1))`, now.AddDate(0, 0, -30)},
		{`DELETE FROM quota_usage WHERE window_start < $1 AND EXISTS (
			SELECT 1 FROM subscriptions s, ` + elems + `
			WHERE s.id = quota_usage.subscription_id AND r.value->>'id' = quota_usage.rule_id
			  AND r.value->'window'->>'kind' IN ('rolling', 'session'))`, now.AddDate(0, 0, -32)},
		{`DELETE FROM quota_usage WHERE window_start < $1 AND EXISTS (
			SELECT 1 FROM subscriptions s, ` + elems + `
			WHERE s.id = quota_usage.subscription_id AND r.value->>'id' = quota_usage.rule_id
			  AND r.value->'window'->>'kind' IN ('calendar', 'period'))`, now.AddDate(0, 0, -400)},
		{`DELETE FROM subscription_charges WHERE created_at < $1`, now.AddDate(0, 0, -90)},
	} {
		tag, err := s.pool.Exec(ctx, q.sql, q.cutoff)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

// RunSweeper calls Sweep every interval until ctx is done.
func (s *Service) RunSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n, err := s.Sweep(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.log.Error("quota usage sweep failed", "err", err)
		} else if n > 0 {
			s.log.Info("quota usage swept", "rows", n)
		}
	}
}
