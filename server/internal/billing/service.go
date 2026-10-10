// Package billing implements wallets, the append-only ledger, balance
// reservations for gateway requests, and redeem codes (ADR-0006/0007/0008).
//
// Every balance change locks the wallet row (SELECT ... FOR UPDATE) and writes a
// ledger entry in the same transaction; wallets.balance_nano is a cache of the
// latest ledger balance_after_nano. Lock order is always
// redeem_codes → redeem_batches → wallets → reservations to avoid deadlocks.
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

// Audit actions written by this package.
const (
	ActionRedeem         = "billing.redeem"
	ActionBatchCreate    = "billing.batch_create"
	ActionBatchUpdate    = "billing.batch_update"
	ActionWalletAdjust   = "billing.wallet_adjust"
	ActionSettingsUpdate = "billing.settings_update"
	ActionSignupCredit   = "billing.signup_credit"
)

const (
	settingEnforce  = "billing.enforce"
	settingCurrency = "billing.currency"

	// DefaultReservationTTL bounds how long a reservation survives a lost Settle.
	DefaultReservationTTL = 15 * time.Minute
	enforceCacheTTL       = 5 * time.Second
	defaultCurrency       = "USD"
)

// Ledger entry kinds and reference types.
const (
	KindGrant  = "grant"
	KindCharge = "charge"
	KindRefund = "refund"
	KindAdjust = "adjust"

	RefRequest = "request"
	RefRedeem  = "redeem"
	RefAdmin   = "admin"
	RefSignup  = "signup"
)

// SettingsSource lets the system settings service own billing.enforce
// (docs/contracts/phase4-api.md §3). Without one, the legacy
// system_settings row 'billing.enforce' is used.
type SettingsSource interface {
	BillingEnforced(ctx context.Context) (bool, error)
	BillingEnforce(ctx context.Context) (bool, int, error)
	SetBillingEnforce(ctx context.Context, actorID uuid.UUID, actorName string, enforce bool, version int, ipPrefix, requestID string) (bool, int, error)
}

// Service owns wallet balances, the ledger, reservations and redeem codes.
type Service struct {
	pool *db.DB
	rec  *audit.Recorder
	log  *slog.Logger
	now  func() time.Time

	// ReservationTTL is how long a reservation lives before SweepExpiredReservations
	// releases it. Set before use; defaults to DefaultReservationTTL.
	ReservationTTL time.Duration

	mu         sync.Mutex
	enforce    bool
	enforceAt  time.Time
	currency   string
	redeemRate *limiter
	settings   SettingsSource
	referral   func(ctx context.Context) ReferralConfig

	// OnLedger, when set, is called after a transaction that changed a wallet
	// balance has committed (notifications: low balance, credits).
	OnLedger func(ctx context.Context, c LedgerChange)
}

// LedgerChange describes one committed balance change.
type LedgerChange struct {
	UserID        uuid.UUID
	EntryID       uuid.UUID
	Kind          string
	RefType       string
	Amount        money.Amount
	BalanceBefore money.Amount
	BalanceAfter  money.Amount
	// Reserved is the wallet's reserved amount after the change.
	Reserved money.Amount
	At       time.Time
}

// dispatch reports the committed changes of w to OnLedger.
func (s *Service) dispatch(ctx context.Context, w *walletRow) {
	if s.OnLedger == nil || w == nil {
		return
	}
	for _, c := range w.changes {
		c.Reserved = w.reserved
		s.OnLedger(ctx, c)
	}
}

// UseSettings delegates billing.enforce to the system settings service.
func (s *Service) UseSettings(src SettingsSource) { s.settings = src }

// NewService creates the billing service.
func NewService(pool *db.DB, rec *audit.Recorder, log *slog.Logger) *Service {
	return &Service{
		pool: pool, rec: rec, log: log,
		now:            func() time.Time { return time.Now().UTC() },
		ReservationTTL: DefaultReservationTTL,
		redeemRate:     newLimiter(rate.Every(time.Minute/5), 5),
	}
}

// RequestMeta carries request context for audit entries.
type RequestMeta struct {
	IPPrefix  string
	RequestID string
}

// Wallet is a user's balance in the settlement currency.
type Wallet struct {
	UserID   uuid.UUID
	Balance  money.Amount
	Reserved money.Amount
	Currency string
	// Version increments on every balance change (not on reservations). A wallet
	// that does not exist yet is reported with version 1, the version it gets
	// when created lazily.
	Version int
}

// Available is the balance not held by in-flight reservations.
func (w Wallet) Available() money.Amount { return w.Balance - w.Reserved }

// LedgerEntry is one append-only balance change.
type LedgerEntry struct {
	ID           uuid.UUID
	Kind         string
	Amount       money.Amount
	BalanceAfter money.Amount
	RefType      string
	RefID        string
	Note         *string
	CreatedBy    *uuid.UUID
	CreatedAt    time.Time
}

// InsufficientBalance is the error returned by Reserve when enforcement blocks a request.
func InsufficientBalance() *apperr.Error {
	return &apperr.Error{Kind: apperr.KindForbidden, Code: "insufficient_balance", Message: "余额不足"}
}

// ---- wallet primitives ----

type walletRow struct {
	id       uuid.UUID
	userID   uuid.UUID
	balance  money.Amount
	reserved money.Amount
	version  int
	changes  []LedgerChange // applied in the current transaction
}

// lockWallet locks the user's wallet row, creating it first when create is set.
// It returns (nil, nil) when the wallet does not exist and create is false.
func (s *Service) lockWallet(ctx context.Context, tx db.Tx, userID uuid.UUID, create bool) (*walletRow, error) {
	if create {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wallets (id, user_id, created_at, updated_at) VALUES ($1, $2, $3, $3)
			ON CONFLICT (user_id) DO NOTHING`, id, userID, s.now()); err != nil {
			return nil, err
		}
	}
	w := walletRow{userID: userID}
	var bal, res int64
	err := tx.QueryRow(ctx, `SELECT id, balance_nano, reserved_nano, version FROM wallets WHERE user_id = $1 FOR UPDATE`,
		userID).Scan(&w.id, &bal, &res, &w.version)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.balance, w.reserved = money.Amount(bal), money.Amount(res)
	return &w, nil
}

var errDuplicateCharge = errors.New("billing: charge already recorded")

// applyLedger appends a ledger entry and updates the (locked) wallet balance.
// It returns errDuplicateCharge when a charge for (refType, refID) already exists.
func (s *Service) applyLedger(ctx context.Context, tx db.Tx, w *walletRow, kind string, amount money.Amount,
	refType, refID string, note *string, createdBy *uuid.UUID) (*LedgerEntry, error) {
	after, err := w.balance.Add(amount)
	if err != nil {
		return nil, apperr.Validation("金额超出可表示范围", nil)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	now := s.now()
	tag, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries (id, wallet_id, kind, amount_nano, balance_after_nano, ref_type, ref_id, note, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT DO NOTHING`,
		id, w.id, kind, int64(amount), int64(after), refType, refID, note, createdBy, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, errDuplicateCharge
	}
	if _, err := tx.Exec(ctx, `UPDATE wallets SET balance_nano = $2, version = version + 1, updated_at = $3 WHERE id = $1`,
		w.id, int64(after), now); err != nil {
		return nil, err
	}
	w.changes = append(w.changes, LedgerChange{UserID: w.userID, EntryID: id, Kind: kind, RefType: refType, Amount: amount,
		BalanceBefore: w.balance, BalanceAfter: after, At: now})
	w.balance = after
	w.version++
	return &LedgerEntry{ID: id, Kind: kind, Amount: amount, BalanceAfter: after, RefType: refType, RefID: refID,
		Note: note, CreatedBy: createdBy, CreatedAt: now}, nil
}

// toWallet builds the API view. currency is passed in (not looked up) so that it is
// never fetched from the pool while a transaction holds a connection.
func toWallet(w *walletRow, userID uuid.UUID, currency string) Wallet {
	out := Wallet{UserID: userID, Currency: currency, Version: 1}
	if w != nil {
		out.Balance, out.Reserved, out.Version = w.balance, w.reserved, w.version
	}
	return out
}

// GetWallet returns the user's wallet; a missing wallet is reported as zero.
func (s *Service) GetWallet(ctx context.Context, userID uuid.UUID) (Wallet, error) {
	var bal, res int64
	var version int
	err := s.pool.QueryRow(ctx, `SELECT balance_nano, reserved_nano, version FROM wallets WHERE user_id = $1`, userID).
		Scan(&bal, &res, &version)
	if db.IsNoRows(err) {
		return toWallet(nil, userID, s.Currency(ctx)), nil
	}
	if err != nil {
		return Wallet{}, err
	}
	return toWallet(&walletRow{balance: money.Amount(bal), reserved: money.Amount(res), version: version}, userID, s.Currency(ctx)), nil
}

// ListLedger returns the user's ledger, newest first.
func (s *Service) ListLedger(ctx context.Context, userID uuid.UUID, offset, limit int) ([]LedgerEntry, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries l JOIN wallets w ON w.id = l.wallet_id
		WHERE w.user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT l.id, l.kind, l.amount_nano, l.balance_after_nano, l.ref_type, l.ref_id, l.note, l.created_by, l.created_at
		FROM ledger_entries l JOIN wallets w ON w.id = l.wallet_id
		WHERE w.user_id = $1 ORDER BY l.created_at DESC, l.id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []LedgerEntry{}
	for rows.Next() {
		var e LedgerEntry
		var amt, after int64
		if err := rows.Scan(&e.ID, &e.Kind, &amt, &after, &e.RefType, &e.RefID, &e.Note, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		e.Amount, e.BalanceAfter, e.CreatedAt = money.Amount(amt), money.Amount(after), e.CreatedAt.UTC()
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// Currency returns the settlement currency code (system_settings billing.currency).
func (s *Service) Currency(ctx context.Context) string {
	s.mu.Lock()
	cur := s.currency
	s.mu.Unlock()
	if cur != "" {
		return cur
	}
	var v struct {
		Code string `json:"code"`
	}
	err := s.pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, settingCurrency).Scan(&v)
	if err != nil || v.Code == "" {
		if err != nil && !db.IsNoRows(err) {
			s.log.WarnContext(ctx, "read settlement currency", "err", err)
		}
		return defaultCurrency
	}
	// The settlement currency is locked after first boot, so caching forever is safe.
	s.mu.Lock()
	s.currency = v.Code
	s.mu.Unlock()
	return v.Code
}

// ---- enforcement setting ----

func parseEnforce(raw []byte) bool {
	var obj struct {
		Enforce bool `json:"enforce"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Enforce
	}
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b
}

// Enforced reports the cached billing.enforce setting (refreshed every ≤5s).
func (s *Service) Enforced(ctx context.Context) (bool, error) {
	if s.settings != nil {
		return s.settings.BillingEnforced(ctx)
	}
	s.mu.Lock()
	if !s.enforceAt.IsZero() && time.Since(s.enforceAt) < enforceCacheTTL {
		v := s.enforce
		s.mu.Unlock()
		return v, nil
	}
	s.mu.Unlock()
	st, err := s.Settings(ctx)
	if err != nil {
		return false, err
	}
	s.setEnforceCache(st.Enforce)
	return st.Enforce, nil
}

func (s *Service) setEnforceCache(v bool) {
	s.mu.Lock()
	s.enforce, s.enforceAt = v, time.Now()
	s.mu.Unlock()
}

// Settings is the billing.enforce system setting.
type Settings struct {
	Enforce bool `json:"enforce"`
	Version int  `json:"version"`
}

// Settings reads billing.enforce uncached. A missing row is {false, 0}.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	if s.settings != nil {
		on, version, err := s.settings.BillingEnforce(ctx)
		return Settings{Enforce: on, Version: version}, err
	}
	var raw []byte
	var st Settings
	err := s.pool.QueryRow(ctx, `SELECT value, version FROM system_settings WHERE key = $1`, settingEnforce).Scan(&raw, &st.Version)
	if db.IsNoRows(err) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	st.Enforce = parseEnforce(raw)
	return st, nil
}

// UpdateSettings writes billing.enforce with optimistic locking on the setting version.
// With a settings source, version is the version of all system settings.
func (s *Service) UpdateSettings(ctx context.Context, actor Actor, enforce bool, version int, meta RequestMeta) (Settings, error) {
	if s.settings != nil {
		on, v, err := s.settings.SetBillingEnforce(ctx, actor.ID, actor.Name, enforce, version, meta.IPPrefix, meta.RequestID)
		return Settings{Enforce: on, Version: v}, err
	}
	var out Settings
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var raw []byte
		var cur int
		err := tx.QueryRow(ctx, `SELECT value, version FROM system_settings WHERE key = $1 FOR UPDATE`, settingEnforce).Scan(&raw, &cur)
		before := false
		val, _ := json.Marshal(map[string]bool{"enforce": enforce})
		now := s.now()
		switch {
		case db.IsNoRows(err):
			if version != 0 {
				return apperr.VersionConflict()
			}
			tag, err := tx.Exec(ctx, `INSERT INTO system_settings (key, value, version, updated_at, updated_by)
				VALUES ($1, $2, 1, $3, $4) ON CONFLICT (key) DO NOTHING`, settingEnforce, val, now, actor.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return apperr.VersionConflict()
			}
			out = Settings{Enforce: enforce, Version: 1}
		case err != nil:
			return err
		default:
			if cur != version {
				return apperr.VersionConflict()
			}
			before = parseEnforce(raw)
			if err := tx.QueryRow(ctx, `UPDATE system_settings SET value = $2, version = version + 1, updated_at = $3, updated_by = $4
				WHERE key = $1 RETURNING version`, settingEnforce, val, now, actor.ID).Scan(&out.Version); err != nil {
				return err
			}
			out.Enforce = enforce
		}
		rid := settingEnforce
		return s.audit(ctx, tx, actor, meta, ActionSettingsUpdate, "system_setting", rid, map[string]any{
			"before": map[string]any{"enforce": before}, "after": map[string]any{"enforce": enforce},
		})
	})
	if err != nil {
		return Settings{}, err
	}
	s.setEnforceCache(out.Enforce)
	return out, nil
}

// GrantSignupCredit credits a new user's wallet once with a 'grant' ledger
// entry (ref signup/<userID>) and audits it. Calling it again for the same
// user is a no-op; amount ≤ 0 does nothing.
func (s *Service) GrantSignupCredit(ctx context.Context, actor Actor, amount money.Amount, meta RequestMeta) error {
	if amount <= 0 {
		return nil
	}
	var w *walletRow
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var err error
		if w, err = s.lockWallet(ctx, tx, actor.ID, true); err != nil {
			return err
		}
		entry, err := s.applyLedger(ctx, tx, w, KindGrant, amount, RefSignup, actor.ID.String(), nil, nil)
		if errors.Is(err, errDuplicateCharge) {
			return nil // already granted
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, ActionSignupCredit, "wallet", actor.ID.String(), map[string]any{
			"userId": actor.ID, "amount": amount.String(), "balanceAfter": w.balance.String(), "ledgerEntryId": entry.ID,
		})
	})
	if err == nil {
		s.dispatch(ctx, w)
	}
	return err
}

// ---- reservations and settlement (called by the gateway) ----

// Reserve holds estimate of the user's available balance for requestID, capped
// at the available balance. When billing.enforce is false it is a no-op. When
// enforced, a request is admitted iff the available balance (balance −
// reserved) is > 0; otherwise it returns InsufficientBalance(). This check
// does not depend on the estimate: an estimate of 0 only checks the balance
// and holds nothing. An estimate above the available balance is never a
// reason to reject (settlement charges the actual cost and may drive the
// balance negative). Reserving the same requestID twice is a no-op.
func (s *Service) Reserve(ctx context.Context, userID uuid.UUID, requestID string, estimate money.Amount) error {
	enforce, err := s.Enforced(ctx)
	if err != nil || !enforce {
		return err
	}
	if requestID == "" {
		return errors.New("billing: empty request id")
	}
	return db.InTx(ctx, s.pool, func(tx db.Tx) error {
		w, err := s.lockWallet(ctx, tx, userID, false)
		if err != nil {
			return err
		}
		if w == nil {
			return InsufficientBalance()
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reservations WHERE request_id = $1)`, requestID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		avail := w.balance - w.reserved
		if avail <= 0 {
			return InsufficientBalance()
		}
		estimate = min(estimate, avail)
		if estimate <= 0 {
			return nil
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `INSERT INTO reservations (request_id, wallet_id, amount_nano, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5)`, requestID, w.id, int64(estimate), now, now.Add(s.ReservationTTL)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE wallets SET reserved_nano = reserved_nano + $2, updated_at = $3 WHERE id = $1`,
			w.id, int64(estimate), now)
		return err
	})
}

// Settle finalizes requestID exactly once: it releases any reservation and, when
// enforced and charge > 0, debits the wallet with a 'charge' ledger entry
// (ref request/requestID). Calling it again never charges twice. The balance may
// go negative when charge exceeds the reservation. When not enforced it only
// releases a stale reservation and never touches balances.
func (s *Service) Settle(ctx context.Context, userID uuid.UUID, requestID string, charge money.Amount) error {
	return s.SettleUsage(ctx, userID, requestID, charge, nil)
}

// SettleUsage is Settle with record, when set, run in the same transaction
// (the usage counters of phase8-api.md §2.2). record runs once per request:
// a repeated settlement of an already charged (or already recorded) request
// skips it, so settlements can be retried and replayed (ADR-0010). A charge
// never depends on a reservation: when the hold is gone (released by the
// sweeper after its TTL) the actual amount is still debited.
func (s *Service) SettleUsage(ctx context.Context, userID uuid.UUID, requestID string, charge money.Amount,
	record func(context.Context, db.Querier) error) error {
	if charge < 0 {
		return fmt.Errorf("billing: negative charge %s", charge)
	}
	if requestID == "" {
		return errors.New("billing: empty request id")
	}
	enforce, err := s.Enforced(ctx)
	if err != nil {
		return err
	}
	if !enforce || charge == 0 {
		return s.release(ctx, requestID, record)
	}
	var w *walletRow
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var err error
		if w, err = s.lockWallet(ctx, tx, userID, true); err != nil {
			return err
		}
		if err := s.releaseLocked(ctx, tx, w, requestID); err != nil {
			return err
		}
		_, err = s.applyLedger(ctx, tx, w, KindCharge, -charge, RefRequest, requestID, nil, nil)
		if errors.Is(err, errDuplicateCharge) {
			return nil // already settled; still commit the (idempotent) release
		}
		if err == nil && record != nil {
			err = record(ctx, tx)
		}
		return err
	})
	if err == nil {
		s.dispatch(ctx, w)
	}
	return err
}

// release drops the reservation for requestID, whichever wallet holds it,
// and runs record (if set) in the same transaction, once per request: the
// request is marked in settled_requests, and a repeated settlement (a retry
// or a journal replay, ADR-0010) skips record.
func (s *Service) release(ctx context.Context, requestID string, record func(context.Context, db.Querier) error) error {
	var walletID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT wallet_id FROM reservations WHERE request_id = $1`, requestID).Scan(&walletID)
	if db.IsNoRows(err) {
		if record == nil {
			return nil
		}
		return db.InTx(ctx, s.pool, func(tx db.Tx) error { return s.recordOnce(ctx, tx, requestID, record) })
	}
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var w walletRow
		if err := tx.QueryRow(ctx, `SELECT id FROM wallets WHERE id = $1 FOR UPDATE`, walletID).Scan(&w.id); err != nil {
			return err
		}
		if err := s.releaseLocked(ctx, tx, &w, requestID); err != nil {
			return err
		}
		return s.recordOnce(ctx, tx, requestID, record)
	})
}

// recordOnce runs record in tx unless requestID was already settled.
func (s *Service) recordOnce(ctx context.Context, tx db.Tx, requestID string, record func(context.Context, db.Querier) error) error {
	if record == nil {
		return nil
	}
	tag, err := tx.Exec(ctx, `INSERT INTO settled_requests (request_id, settled_at) VALUES ($1, $2)
		ON CONFLICT (request_id) DO NOTHING`, requestID, s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // already recorded
	}
	return record(ctx, tx)
}

// settledRetention is how long settled_requests rows are kept.
const settledRetention = 60 * 24 * time.Hour

// PruneSettled deletes settled_requests rows older than 60 days (daily,
// with the request-log retention job).
func (s *Service) PruneSettled(ctx context.Context, now time.Time) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM settled_requests WHERE settled_at < $1`, now.Add(-settledRetention))
	if err != nil {
		if ctx.Err() == nil {
			s.log.ErrorContext(ctx, "settled request pruning failed", "err", err)
		}
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		s.log.InfoContext(ctx, "settled requests pruned", "deleted", n)
	}
}

// releaseLocked deletes requestID's reservation on the locked wallet w.
func (s *Service) releaseLocked(ctx context.Context, tx db.Tx, w *walletRow, requestID string) error {
	var amt int64
	err := tx.QueryRow(ctx, `DELETE FROM reservations WHERE request_id = $1 AND wallet_id = $2 RETURNING amount_nano`,
		requestID, w.id).Scan(&amt)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE wallets SET reserved_nano = GREATEST(reserved_nano - $2, 0), updated_at = $3 WHERE id = $1`,
		w.id, amt, s.now())
	w.reserved = max(w.reserved-money.Amount(amt), 0)
	return err
}

// SweepExpiredReservations releases reservations past expires_at and returns how
// many were released.
func (s *Service) SweepExpiredReservations(ctx context.Context) (int, error) {
	const batch = 500
	total := 0
	for {
		now := s.now()
		rows, err := s.pool.Query(ctx, `SELECT DISTINCT wallet_id FROM reservations WHERE expires_at < $1 LIMIT $2`, now, batch)
		if err != nil {
			return total, err
		}
		ids, err := db.CollectRows[uuid.UUID](rows)
		if err != nil {
			return total, err
		}
		for _, wid := range ids {
			err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
				if _, err := tx.Exec(ctx, `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, wid); err != nil {
					return err
				}
				rows, err := tx.Query(ctx, `DELETE FROM reservations WHERE wallet_id = $1 AND expires_at < $2
					RETURNING amount_nano`, wid, now)
				if err != nil {
					return err
				}
				amounts, err := db.CollectRows[int64](rows)
				if err != nil {
					return err
				}
				n := len(amounts)
				var sum int64
				for _, a := range amounts {
					sum += a
				}
				if n == 0 {
					return nil
				}
				if _, err := tx.Exec(ctx, `UPDATE wallets SET reserved_nano = GREATEST(reserved_nano - $2, 0), updated_at = $3
					WHERE id = $1`, wid, sum, now); err != nil {
					return err
				}
				total += n
				return nil
			})
			if err != nil {
				return total, err
			}
		}
		if len(ids) < batch {
			return total, nil
		}
	}
}

// RunSweeper calls SweepExpiredReservations every interval until ctx is done.
func (s *Service) RunSweeper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.SweepExpiredReservations(ctx)
			if err != nil && ctx.Err() == nil {
				s.log.ErrorContext(ctx, "sweep expired reservations", "err", err)
			} else if n > 0 {
				s.log.InfoContext(ctx, "released expired reservations", "count", n)
			}
		}
	}
}

// ---- admin wallet operations ----

func (s *Service) userExists(ctx context.Context, q db.Querier, userID uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, userID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("用户")
	}
	return nil
}

// AdminWallet returns any user's wallet (404 when the user does not exist).
func (s *Service) AdminWallet(ctx context.Context, userID uuid.UUID) (Wallet, error) {
	if err := s.userExists(ctx, s.pool, userID); err != nil {
		return Wallet{}, err
	}
	return s.GetWallet(ctx, userID)
}

// AdjustInput is a manual balance correction by an administrator.
type AdjustInput struct {
	Amount  money.Amount // non-zero; negative debits
	Note    string       // required
	Version int          // expected wallet version
}

// AdjustWallet applies an 'adjust' ledger entry with optimistic locking on the wallet version.
func (s *Service) AdjustWallet(ctx context.Context, actor Actor, userID uuid.UUID, in AdjustInput, meta RequestMeta) (Wallet, *LedgerEntry, error) {
	details := map[string]any{}
	if in.Amount == 0 {
		details["amount"] = "不能为 0"
	}
	if in.Note == "" {
		details["note"] = "必填"
	} else if len([]rune(in.Note)) > maxNoteLen {
		details["note"] = fmt.Sprintf("不能超过 %d 个字符", maxNoteLen)
	}
	if len(details) > 0 {
		return Wallet{}, nil, apperr.Validation("参数校验失败", details)
	}
	currency := s.Currency(ctx)
	var w *walletRow
	var entry *LedgerEntry
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if err := s.userExists(ctx, tx, userID); err != nil {
			return err
		}
		var err error
		if w, err = s.lockWallet(ctx, tx, userID, true); err != nil {
			return err
		}
		if w.version != in.Version {
			return apperr.VersionConflict()
		}
		before := w.balance
		refID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		note := in.Note
		if entry, err = s.applyLedger(ctx, tx, w, KindAdjust, in.Amount, RefAdmin, refID.String(), &note, &actor.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, ActionWalletAdjust, "wallet", userID.String(), map[string]any{
			"userId": userID, "amount": in.Amount.String(), "balanceBefore": before.String(),
			"balanceAfter": w.balance.String(), "note": in.Note, "ledgerEntryId": entry.ID,
		})
	})
	if err != nil {
		return Wallet{}, nil, err
	}
	s.dispatch(ctx, w)
	return toWallet(w, userID, currency), entry, nil
}

// ---- audit and rate limiting helpers ----

// Actor identifies who performs an audited operation.
type Actor struct {
	ID   uuid.UUID
	Name string
}

func (s *Service) audit(ctx context.Context, q db.Querier, actor Actor, meta RequestMeta, action, resType, resID string, md map[string]any) error {
	id, name := actor.ID, actor.Name
	return s.rec.Record(ctx, q, audit.Entry{
		ActorID: &id, ActorName: &name, Action: action, ResourceType: resType, ResourceID: &resID,
		IPPrefix: meta.IPPrefix, RequestID: meta.RequestID, Metadata: md,
	})
}

// limiter is a per-key token bucket (in-process; one instance per process).
type limiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	buckets map[string]*rate.Limiter
	sweep   time.Time
}

func newLimiter(every rate.Limit, burst int) *limiter {
	return &limiter{every: every, burst: burst, buckets: map[string]*rate.Limiter{}, sweep: time.Now()}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.sweep) > 10*time.Minute {
		for k, b := range l.buckets {
			if b.Tokens() >= float64(l.burst) {
				delete(l.buckets, k)
			}
		}
		l.sweep = time.Now()
	}
	b, ok := l.buckets[key]
	if !ok {
		b = rate.NewLimiter(l.every, l.burst)
		l.buckets[key] = b
	}
	return b.Allow()
}
