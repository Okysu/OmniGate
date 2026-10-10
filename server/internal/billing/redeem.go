package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/subscription"
)

const (
	BatchKindWalletCredit = "wallet_credit"
	BatchKindPlan         = "plan"
	BatchActive           = "active"
	BatchDisabled         = "disabled"

	MaxBatchCount = 1000
	maxNoteLen    = 500
)

// Batch is a redeem code batch. Amount is set for wallet_credit batches;
// PlanID/PlanName/Periods for plan batches.
type Batch struct {
	ID                    uuid.UUID
	Kind                  string
	Amount                money.Amount
	PlanID                *uuid.UUID
	PlanName              *string
	Periods               *int
	Count                 int
	Redeemed              int
	MaxRedemptionsPerCode int
	PerUserLimit          int
	ValidFrom             *time.Time
	ExpiresAt             *time.Time
	Note                  *string
	Status                string
	CreatedAt             time.Time
	CreatedBy             uuid.UUID
	Version               int
}

func redeemInvalid() *apperr.Error {
	return apperr.New(apperr.KindValidation, "redeem_invalid", "兑换码无效")
}

func redeemConflict(code, msg string) *apperr.Error {
	return apperr.New(apperr.KindConflict, code, msg)
}

const batchCols = `b.id, b.kind, b.amount_nano, b.count,
	(SELECT count(*) FROM redemptions r WHERE r.batch_id = b.id),
	b.max_redemptions_per_code, b.per_user_limit, b.valid_from, b.expires_at, b.note, b.status,
	b.created_at, b.created_by, b.version,
	b.plan_id, (SELECT p.name FROM plans p WHERE p.id = b.plan_id), b.periods`

func scanBatch(row db.Row) (*Batch, error) {
	var b Batch
	var amt *int64
	if err := row.Scan(&b.ID, &b.Kind, &amt, &b.Count, &b.Redeemed, &b.MaxRedemptionsPerCode, &b.PerUserLimit,
		&b.ValidFrom, &b.ExpiresAt, &b.Note, &b.Status, &b.CreatedAt, &b.CreatedBy, &b.Version,
		&b.PlanID, &b.PlanName, &b.Periods); err != nil {
		return nil, err
	}
	if amt != nil {
		b.Amount = money.Amount(*amt)
	}
	b.CreatedAt = b.CreatedAt.UTC()
	b.ValidFrom, b.ExpiresAt = utcPtr(b.ValidFrom), utcPtr(b.ExpiresAt)
	return &b, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// BatchInput describes a new batch: wallet_credit (Amount) or plan (PlanID, Periods).
type BatchInput struct {
	Kind                  string // "" → wallet_credit
	Amount                money.Amount
	PlanID                *uuid.UUID
	Periods               int
	Count                 int
	MaxRedemptionsPerCode int // 0 → 1
	PerUserLimit          int // 0 → 1
	ValidFrom             *time.Time
	ExpiresAt             *time.Time
	Note                  *string
}

// CreateBatch generates a batch of codes. The plaintext codes are returned only
// here and never stored or logged.
func (s *Service) CreateBatch(ctx context.Context, actor Actor, in BatchInput, meta RequestMeta) (*Batch, []string, error) {
	if in.MaxRedemptionsPerCode == 0 {
		in.MaxRedemptionsPerCode = 1
	}
	if in.PerUserLimit == 0 {
		in.PerUserLimit = 1
	}
	if in.Kind == "" {
		in.Kind = BatchKindWalletCredit
	}
	now := s.now()
	details := map[string]any{}
	switch in.Kind {
	case BatchKindWalletCredit:
		if in.Amount <= 0 {
			details["amount"] = "必须大于 0"
		}
		if in.PlanID != nil {
			details["planId"] = "仅 plan 类型可用"
		}
		if in.Periods != 0 {
			details["periods"] = "仅 plan 类型可用"
		}
	case BatchKindPlan:
		if in.Amount != 0 {
			details["amount"] = "plan 类型不能设置金额"
		}
		if in.PlanID == nil {
			details["planId"] = "必填"
		}
		if in.Periods < 1 || in.Periods > subscription.MaxPeriods {
			details["periods"] = fmt.Sprintf("必须在 1 到 %d 之间", subscription.MaxPeriods)
		}
	default:
		details["kind"] = "必须为 wallet_credit 或 plan"
	}
	if in.Count < 1 || in.Count > MaxBatchCount {
		details["count"] = fmt.Sprintf("必须在 1 到 %d 之间", MaxBatchCount)
	}
	if in.MaxRedemptionsPerCode < 1 || in.MaxRedemptionsPerCode > 1_000_000 {
		details["maxRedemptionsPerCode"] = "必须在 1 到 1000000 之间"
	}
	if in.PerUserLimit < 1 || in.PerUserLimit > 1_000_000 {
		details["perUserLimit"] = "必须在 1 到 1000000 之间"
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		details["expiresAt"] = "必须晚于当前时间"
	}
	if in.ValidFrom != nil && in.ExpiresAt != nil && !in.ExpiresAt.After(*in.ValidFrom) {
		details["expiresAt"] = "必须晚于 validFrom"
	}
	if in.Note != nil && len([]rune(*in.Note)) > maxNoteLen {
		details["note"] = fmt.Sprintf("不能超过 %d 个字符", maxNoteLen)
	}
	if len(details) > 0 {
		return nil, nil, apperr.Validation("参数校验失败", details)
	}
	if in.Note != nil && *in.Note == "" {
		in.Note = nil
	}
	batchID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, err
	}
	codes := make([]string, 0, in.Count)
	rows := make([][]any, 0, in.Count)
	for range in.Count {
		display, norm, err := GenerateCode()
		if err != nil {
			return nil, nil, err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return nil, nil, err
		}
		codes = append(codes, display)
		rows = append(rows, []any{id, batchID, HashCode(norm), CodePrefix(norm), now})
	}
	var amount *int64
	var periods *int
	if in.Kind == BatchKindPlan {
		periods = &in.Periods
	} else {
		amount = ptr(int64(in.Amount))
		in.PlanID = nil
	}
	var b *Batch
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if in.PlanID != nil {
			var status string
			err := tx.QueryRow(ctx, `SELECT status FROM plans WHERE id = $1 FOR SHARE`, *in.PlanID).Scan(&status)
			if db.IsNoRows(err) {
				return apperr.Validation("参数校验失败", map[string]any{"planId": "套餐不存在"})
			}
			if err != nil {
				return err
			}
			if status != subscription.PlanActive {
				return subscription.PlanArchivedError()
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO redeem_batches (id, kind, amount_nano, plan_id, periods, count, max_redemptions_per_code,
				per_user_limit, valid_from, expires_at, note, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)`,
			batchID, in.Kind, amount, in.PlanID, periods, in.Count, in.MaxRedemptionsPerCode, in.PerUserLimit,
			in.ValidFrom, in.ExpiresAt, in.Note, actor.ID, now); err != nil {
			return err
		}
		if _, err := tx.CopyFrom(ctx, "redeem_codes",
			[]string{"id", "batch_id", "code_hash", "prefix", "created_at"}, rows); err != nil {
			return err
		}
		var err error
		if b, err = scanBatch(tx.QueryRow(ctx, `SELECT `+batchCols+` FROM redeem_batches b WHERE b.id = $1`, batchID)); err != nil {
			return err
		}
		md := map[string]any{
			"kind": b.Kind, "count": b.Count,
			"maxRedemptionsPerCode": b.MaxRedemptionsPerCode, "perUserLimit": b.PerUserLimit,
			"validFrom": b.ValidFrom, "expiresAt": b.ExpiresAt, "note": b.Note,
		}
		if b.Kind == BatchKindPlan {
			md["planId"], md["periods"] = b.PlanID, b.Periods
		} else {
			md["amount"] = b.Amount.String()
		}
		return s.audit(ctx, tx, actor, meta, ActionBatchCreate, "redeem_batch", batchID.String(), md)
	})
	if err != nil {
		return nil, nil, err
	}
	return b, codes, nil
}

// ListBatches returns batches newest first with redemption counts.
func (s *Service) ListBatches(ctx context.Context, offset, limit int) ([]*Batch, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM redeem_batches`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+batchCols+` FROM redeem_batches b
		ORDER BY b.created_at DESC, b.id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Batch{}
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, b)
	}
	return out, total, rows.Err()
}

// UpdateBatchStatus enables or disables a batch. version is optional (nil skips
// the optimistic lock check).
func (s *Service) UpdateBatchStatus(ctx context.Context, actor Actor, id uuid.UUID, status string, version *int, meta RequestMeta) (*Batch, error) {
	if status != BatchActive && status != BatchDisabled {
		return nil, apperr.Validation("参数校验失败", map[string]any{"status": "必须为 active 或 disabled"})
	}
	var b *Batch
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var cur string
		var curVersion int
		err := tx.QueryRow(ctx, `SELECT status, version FROM redeem_batches WHERE id = $1 FOR UPDATE`, id).Scan(&cur, &curVersion)
		if db.IsNoRows(err) {
			return apperr.NotFound("兑换码批次")
		}
		if err != nil {
			return err
		}
		if version != nil && *version != curVersion {
			return apperr.VersionConflict()
		}
		if _, err := tx.Exec(ctx, `UPDATE redeem_batches SET status = $2, version = version + 1, updated_at = $3 WHERE id = $1`,
			id, status, s.now()); err != nil {
			return err
		}
		if b, err = scanBatch(tx.QueryRow(ctx, `SELECT `+batchCols+` FROM redeem_batches b WHERE b.id = $1`, id)); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, ActionBatchUpdate, "redeem_batch", id.String(), map[string]any{
			"before": map[string]any{"status": cur}, "after": map[string]any{"status": status},
		})
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// RedeemResult is the outcome of a successful redemption. Kind is the batch
// kind: wallet_credit sets Amount/Wallet/Entry; plan sets Subscription/Renewed.
type RedeemResult struct {
	Kind         string
	Amount       money.Amount
	Wallet       Wallet
	Entry        *LedgerEntry
	Subscription *subscription.SubscriptionView
	Renewed      bool
}

// Redeem atomically redeems a code for actor: lock the code, validate the batch,
// record the redemption, then credit the wallet with a 'grant' entry
// (wallet_credit) or grant/renew the plan (plan), and audit. Attempts
// are rate limited per user (5/min). Unknown or malformed codes always yield
// redeem_invalid; detailed errors are only returned for codes that exist.
func (s *Service) Redeem(ctx context.Context, actor Actor, code string, meta RequestMeta) (*RedeemResult, error) {
	if !s.redeemRate.allow(actor.ID.String()) {
		return nil, apperr.RateLimited()
	}
	norm, ok := NormalizeCode(code)
	if !ok {
		return nil, redeemInvalid()
	}
	currency := s.Currency(ctx)
	var res RedeemResult
	var credited *walletRow
	var inviter *walletRow
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		credited = nil
		inviter = nil
		var codeID, batchID uuid.UUID
		var prefix string
		var used int
		err := tx.QueryRow(ctx, `SELECT id, batch_id, prefix, used_count FROM redeem_codes WHERE code_hash = $1 FOR UPDATE`,
			HashCode(norm)).Scan(&codeID, &batchID, &prefix, &used)
		if db.IsNoRows(err) {
			return redeemInvalid()
		}
		if err != nil {
			return err
		}
		var kind, status string
		var amt *int64
		var planID *uuid.UUID
		var periods *int
		var maxPerCode, perUser int
		var validFrom, expiresAt *time.Time
		// FOR SHARE makes a concurrent disable wait for in-flight redemptions.
		if err := tx.QueryRow(ctx, `SELECT kind, amount_nano, plan_id, periods, max_redemptions_per_code, per_user_limit,
				valid_from, expires_at, status
			FROM redeem_batches WHERE id = $1 FOR SHARE`, batchID).
			Scan(&kind, &amt, &planID, &periods, &maxPerCode, &perUser, &validFrom, &expiresAt, &status); err != nil {
			return err
		}
		now := s.now()
		switch {
		case status != BatchActive:
			return redeemConflict("redeem_batch_disabled", "该兑换码所属批次已停用")
		case validFrom != nil && now.Before(*validFrom):
			return redeemConflict("redeem_not_started", "该兑换码尚未到生效时间")
		case expiresAt != nil && !now.Before(*expiresAt):
			return redeemConflict("redeem_expired", "该兑换码已过期")
		case used >= maxPerCode:
			return redeemConflict("redeem_used_up", "该兑换码已被使用")
		}
		if kind == BatchKindPlan && planID != nil && periods != nil {
			return s.redeemPlan(ctx, tx, actor, meta, &res, codeID, batchID, prefix, perUser, *planID, *periods, now)
		}
		if kind != BatchKindWalletCredit || amt == nil {
			return fmt.Errorf("billing: unsupported redeem batch kind %q", kind)
		}
		// The wallet lock also serializes this user's redemptions, which makes the
		// per-user count below race-free across different codes of one batch.
		w, err := s.lockWallet(ctx, tx, actor.ID, true)
		if err != nil {
			return err
		}
		var mine int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM redemptions WHERE batch_id = $1 AND user_id = $2`,
			batchID, actor.ID).Scan(&mine); err != nil {
			return err
		}
		if mine >= perUser {
			return redeemConflict("redeem_user_limit", "已达到该批次的兑换次数上限")
		}
		redemptionID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		note := "兑换码 " + prefix
		entry, err := s.applyLedger(ctx, tx, w, KindGrant, money.Amount(*amt), RefRedeem, redemptionID.String(), &note, nil)
		if err != nil {
			return err
		}
		if inviter, err = s.referralRebate(ctx, tx, actor, redemptionID, money.Amount(*amt)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO redemptions (id, code_id, batch_id, user_id, ledger_entry_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, redemptionID, codeID, batchID, actor.ID, entry.ID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE redeem_codes SET used_count = used_count + 1 WHERE id = $1`, codeID); err != nil {
			return err
		}
		res = RedeemResult{Kind: BatchKindWalletCredit, Amount: money.Amount(*amt), Wallet: toWallet(w, actor.ID, currency), Entry: entry}
		credited = w
		// Never audit the plaintext code; the display prefix is enough for support.
		return s.audit(ctx, tx, actor, meta, ActionRedeem, "redeem_code", codeID.String(), map[string]any{
			"batchId": batchID, "codePrefix": prefix, "amount": res.Amount.String(),
			"redemptionId": redemptionID, "ledgerEntryId": entry.ID,
		})
	})
	if err != nil {
		return nil, err
	}
	s.dispatch(ctx, credited)
	s.dispatch(ctx, inviter)
	return &res, nil
}

// redeemPlan finishes a plan redemption inside Redeem's transaction: the
// per-user advisory lock serializes this user's redemptions (making the
// per-user count race-free), then the plan is granted or renewed.
func (s *Service) redeemPlan(ctx context.Context, tx db.Tx, actor Actor, meta RequestMeta, res *RedeemResult,
	codeID, batchID uuid.UUID, prefix string, perUser int, planID uuid.UUID, periods int, now time.Time) error {
	if err := subscription.LockUser(ctx, tx, actor.ID); err != nil {
		return err
	}
	var mine int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM redemptions WHERE batch_id = $1 AND user_id = $2`,
		batchID, actor.ID).Scan(&mine); err != nil {
		return err
	}
	if mine >= perUser {
		return redeemConflict("redeem_user_limit", "已达到该批次的兑换次数上限")
	}
	redemptionID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	sub, renewed, err := subscription.GrantTx(ctx, tx, now, actor.ID, planID, periods, subscription.SourceRedeem,
		redemptionID.String())
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO redemptions (id, code_id, batch_id, user_id, subscription_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, redemptionID, codeID, batchID, actor.ID, sub.ID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE redeem_codes SET used_count = used_count + 1 WHERE id = $1`, codeID); err != nil {
		return err
	}
	view, err := subscription.DescribeTx(ctx, tx, sub, now)
	if err != nil {
		return err
	}
	*res = RedeemResult{Kind: BatchKindPlan, Subscription: view, Renewed: renewed}
	return s.audit(ctx, tx, actor, meta, ActionRedeem, "redeem_code", codeID.String(), map[string]any{
		"batchId": batchID, "codePrefix": prefix, "kind": BatchKindPlan, "planId": planID, "periods": periods,
		"subscriptionId": sub.ID, "renewed": renewed, "endsAt": sub.EndsAt, "redemptionId": redemptionID,
	})
}

func ptr[T any](v T) *T { return &v }
