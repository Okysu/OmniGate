// Package lottery records paid model usage in the settlement transaction.
package lottery

import (
	"context"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

func Day(at time.Time) string { return at.UTC().Add(8 * time.Hour).Format(time.DateOnly) }

// Record runs only after the caller's unique financial entry was inserted.
func Record(ctx context.Context, tx db.Tx, user uuid.UUID, spend money.Amount, at time.Time) error {
	if spend <= 0 {
		return nil
	}
	var threshold int64
	if err := tx.QueryRow(ctx, "SELECT threshold_nano FROM lottery_config WHERE id=1 FOR SHARE").Scan(&threshold); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO lottery_days (user_id,day,spent_nano,earned) VALUES ($1,$2,$3,$4)
  ON CONFLICT(user_id,day) DO UPDATE SET spent_nano=lottery_days.spent_nano+excluded.spent_nano,
  earned=lottery_days.earned OR (lottery_days.spent_nano+excluded.spent_nano > $5)`, user, Day(at), int64(spend), int64(spend) > threshold, threshold)
	return err
}
