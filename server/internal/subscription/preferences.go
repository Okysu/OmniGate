package subscription

import (
	"context"
	"errors"

	"omnigate/internal/platform/db"

	"github.com/google/uuid"
)

// Preferences are a user's billing preferences.
type Preferences struct {
	QuotaOverflow string `json:"quotaOverflow"` // "block" | "wallet"
}

// QuotaOverflow returns the user's preference (default "block").
func (s *Service) QuotaOverflow(ctx context.Context, userID uuid.UUID) (string, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT quota_overflow FROM billing_preferences WHERE user_id = $1`, userID).Scan(&v)
	if errors.Is(err, db.ErrNoRows) {
		return OverflowBlock, nil
	}
	return v, err
}

// SetPreferences stores the user's preferences and audits the change.
func (s *Service) SetPreferences(ctx context.Context, a Actor, p Preferences, m RequestMeta) (*Preferences, error) {
	if p.QuotaOverflow != OverflowBlock && p.QuotaOverflow != OverflowWallet {
		return nil, invalid(map[string]any{"quotaOverflow": "必须为 block 或 wallet"})
	}
	old, err := s.QuotaOverflow(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO billing_preferences (user_id, quota_overflow) VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET quota_overflow = EXCLUDED.quota_overflow, updated_at = now()`, a.ID, p.QuotaOverflow); err != nil {
			return err
		}
		if old == p.QuotaOverflow {
			return nil
		}
		return s.audit(ctx, tx, a, m, ActionPrefs, "user", a.ID.String(), map[string]any{"quotaOverflow": map[string]string{"from": old, "to": p.QuotaOverflow}})
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}
