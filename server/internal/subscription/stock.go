package subscription

import (
	"context"
	"github.com/google/uuid"
	"omnigate/internal/apperr"
	"omnigate/internal/platform/db"
)

func stockValue(v *int64) int64 {
	if v == nil {
		return -1
	}
	return *v
}

// ConsumeStockTx reserves stock in the same transaction as payment and grant.
// Unlimited plans do not mutate their edit version on every purchase.
func ConsumeStockTx(ctx context.Context, tx db.Tx, id uuid.UUID, quantity int64) error {
	if quantity < 1 {
		return apperr.Validation("数量必须大于零", nil)
	}
	var stock int64
	if err := tx.QueryRow(ctx, `SELECT stock FROM plans WHERE id=$1 FOR UPDATE`, id).Scan(&stock); err != nil {
		return err
	}
	if stock == -1 {
		return nil
	}
	if stock < quantity {
		return apperr.New(apperr.KindConflict, "plan_sold_out", "套餐库存不足")
	}
	_, err := tx.Exec(ctx, `UPDATE plans SET stock=stock-$2,version=version+1 WHERE id=$1`, id, quantity)
	return err
}
