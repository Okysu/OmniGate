package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/billing"
	"omnigate/internal/money"
	"omnigate/internal/subscription"
)

// accountSource feeds the gateway's key-authenticated account endpoints
// (GET /v1/account) from the billing and subscription services.
type accountSource struct {
	bill *billing.Service
	subs *subscription.Service
}

func (s accountSource) Wallet(ctx context.Context, userID uuid.UUID) (money.Amount, money.Amount, string, error) {
	w, err := s.bill.GetWallet(ctx, userID)
	return w.Balance, w.Reserved, w.Currency, err
}

func (s accountSource) ActiveSubscriptions(ctx context.Context, userID uuid.UUID) ([]*subscription.SubscriptionView, error) {
	return s.subs.ActiveViews(ctx, userID, time.Now())
}

func (s accountSource) QuotaOverflow(ctx context.Context, userID uuid.UUID) (string, error) {
	return s.subs.QuotaOverflow(ctx, userID)
}
