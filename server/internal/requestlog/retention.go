package requestlog

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"omnigate/internal/platform/db"
)

// PruneResult describes one retention run.
type PruneResult struct {
	Cutoff            time.Time
	DroppedPartitions []string
	DeletedRows       int64
}

// Prune enforces a retention of days (0 = keep everything). On PostgreSQL
// monthly partitions lying entirely before the cutoff are dropped, and older
// rows in the boundary partition (and the default partition) are deleted; on
// SQLite (one table) old rows are deleted. The cutoff is computed in Go so no
// interval arithmetic is needed in SQL.
func Prune(ctx context.Context, pool *db.DB, now time.Time, days int) (PruneResult, error) {
	if days <= 0 {
		return PruneResult{}, nil
	}
	res := PruneResult{Cutoff: now.UTC().Add(-time.Duration(days) * 24 * time.Hour)}
	if pool.Dialect() == db.Postgres {
		if err := dropPartitions(ctx, pool, &res); err != nil {
			return res, err
		}
	}
	tag, err := pool.Exec(ctx, `DELETE FROM request_logs WHERE started_at < $1`, res.Cutoff)
	if err != nil {
		return res, err
	}
	res.DeletedRows = tag.RowsAffected()
	return res, nil
}

// dropPartitions drops the monthly partitions that end before the cutoff.
func dropPartitions(ctx context.Context, pool *db.DB, res *PruneResult) error {
	rows, err := pool.Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'request_logs'::regclass`)
	if err != nil {
		return err
	}
	names, err := db.CollectRows[string](rows)
	if err != nil {
		return err
	}
	for _, name := range names {
		var y, m int
		if n, err := fmt.Sscanf(name, "request_logs_y%4dm%2d", &y, &m); err != nil || n != 2 || m < 1 || m > 12 ||
			name != fmt.Sprintf("request_logs_y%04dm%02d", y, m) {
			continue // the default partition or a foreign table
		}
		end := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		if end.After(res.Cutoff) {
			continue
		}
		// name matched the strict pattern above, so it is a safe identifier.
		if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS "`+name+`"`); err != nil {
			return fmt.Errorf("drop partition %s: %w", name, err)
		}
		res.DroppedPartitions = append(res.DroppedPartitions, name)
	}
	return nil
}

// RunRetention applies the retention setting once at start and then daily.
//
// extra jobs run on the same daily schedule (e.g. notification retention).
func RunRetention(ctx context.Context, pool *db.DB, log *slog.Logger, days func(context.Context) int, extra ...func(context.Context, time.Time)) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		for _, f := range extra {
			f(ctx, time.Now().UTC())
		}
		res, err := Prune(ctx, pool, time.Now(), days(ctx))
		switch {
		case err != nil && ctx.Err() == nil:
			log.Error("request log retention failed", "err", err)
		case len(res.DroppedPartitions) > 0 || res.DeletedRows > 0:
			log.Info("request log retention applied", "cutoff", res.Cutoff, "dropped_partitions", res.DroppedPartitions,
				"deleted_rows", res.DeletedRows)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
