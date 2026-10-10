package billing

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

// Referral rebates (docs/contracts/phase15-api.md §4): every user has a
// permanent invite code; an account created through an invite link is bound to
// its inviter, and each qualifying wallet_credit redemption of the invitee
// credits the inviter a percentage of the recharge.

// RefReferral is the ledger reference type of referral rebates.
const RefReferral = "referral"

// ReferralConfig is the effective billing.referral* settings.
type ReferralConfig struct {
	Enabled bool
	// Rate is the percentage as entered ("10", "12.5"); RateBP is it in basis
	// points (1/100 of a percent).
	Rate        string
	RateBP      int64
	MinRecharge money.Amount
}

// UseReferral sets the source of the referral settings (without one, rebates
// are off).
func (s *Service) UseReferral(f func(ctx context.Context) ReferralConfig) { s.referral = f }

func (s *Service) referralConfig(ctx context.Context) ReferralConfig {
	if s.referral == nil {
		return ReferralConfig{Rate: "0"}
	}
	return s.referral(ctx)
}

// RebateAmount is recharge × rateBP / 10000, rounded half away from zero.
func RebateAmount(recharge money.Amount, rateBP int64) money.Amount {
	if recharge <= 0 || rateBP <= 0 {
		return 0
	}
	a, err := recharge.MulDiv(rateBP, 10000)
	if err != nil {
		return 0
	}
	return a
}

// inviteAlphabet avoids look-alike characters (0/O, 1/I/L).
const inviteAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

const inviteCodeLen = 8

func newInviteCode() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(inviteAlphabet)))
	for range inviteCodeLen {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(inviteAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// NormalizeInviteCode upper-cases and validates an invite code from a URL.
func NormalizeInviteCode(code string) (string, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != inviteCodeLen {
		return "", false
	}
	for _, c := range code {
		if !strings.ContainsRune(inviteAlphabet, c) {
			return "", false
		}
	}
	return code, true
}

// InviteCode returns the user's invite code, creating it on first use.
func (s *Service) InviteCode(ctx context.Context, userID uuid.UUID) (string, error) {
	for range 5 {
		var code string
		err := s.pool.QueryRow(ctx, `SELECT code FROM referral_codes WHERE user_id = $1`, userID).Scan(&code)
		if err == nil {
			return code, nil
		}
		if !db.IsNoRows(err) {
			return "", err
		}
		if code, err = newInviteCode(); err != nil {
			return "", err
		}
		// A conflict on user_id (a concurrent first use) or on code (a collision)
		// inserts nothing; the loop then reads the winner or retries.
		if _, err := s.pool.Exec(ctx, `INSERT INTO referral_codes (user_id, code, created_at) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, userID, code, s.now()); err != nil {
			return "", err
		}
	}
	return "", errInviteCode
}

var errInviteCode = errors.New("billing: could not allocate an invite code")

// BindReferral binds a newly created user to the owner of code. Unknown codes,
// self-invites and already bound users are ignored (binding is best effort and
// never blocks a sign-up).
func (s *Service) BindReferral(ctx context.Context, inviteeID uuid.UUID, code string) error {
	code, ok := NormalizeInviteCode(code)
	if !ok {
		return nil
	}
	var inviter uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT user_id FROM referral_codes WHERE code = $1`, code).Scan(&inviter)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if inviter == inviteeID {
		return nil
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO referrals (invitee_id, inviter_id, created_at) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, inviteeID, inviter, s.now())
	return err
}

// referralRebate credits the inviter of invitee for a wallet_credit redemption
// of amount inside Redeem's transaction, after the invitee's wallet is locked.
// The inviter registered before the invitee, so the referral graph is acyclic
// and invitee wallet → inviter wallet never deadlocks. It returns the inviter's
// wallet (for dispatch) or nil when no rebate applies.
func (s *Service) referralRebate(ctx context.Context, tx db.Tx, invitee Actor, redemptionID uuid.UUID,
	amount money.Amount) (*walletRow, error) {
	cfg := s.referralConfig(ctx)
	if !cfg.Enabled || amount < cfg.MinRecharge {
		return nil, nil
	}
	rebate := RebateAmount(amount, cfg.RateBP)
	if rebate <= 0 {
		return nil, nil
	}
	var inviter uuid.UUID
	err := tx.QueryRow(ctx, `SELECT inviter_id FROM referrals WHERE invitee_id = $1`, invitee.ID).Scan(&inviter)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w, err := s.lockWallet(ctx, tx, inviter, true)
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	note := "邀请返利 " + MaskName(invitee.Name)
	entry, err := s.applyLedger(ctx, tx, w, KindGrant, rebate, RefReferral, id.String(), &note, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO referral_rebates (id, inviter_id, invitee_id, redemption_id, recharge_nano, rate,
			rebate_nano, ledger_entry_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, inviter, invitee.ID, redemptionID, int64(amount), cfg.Rate, int64(rebate), entry.ID, s.now()); err != nil {
		return nil, err
	}
	return w, nil
}

// MaskName keeps the first and last character of a display name and stars the
// middle (at most 4 stars); names of up to 2 characters keep the first one.
func MaskName(name string) string {
	r := []rune(strings.TrimSpace(name))
	switch {
	case len(r) == 0:
		return "*"
	case len(r) <= 2:
		return string(r[0]) + "*"
	}
	stars := min(len(r)-2, 4)
	return string(r[0]) + strings.Repeat("*", stars) + string(r[len(r)-1])
}

// ReferralInfo is GET /api/billing/referral (without the link, which the
// handler builds from the public URL).
type ReferralInfo struct {
	Config       ReferralConfig
	Code         string
	InvitedCount int
	RebateTotal  money.Amount
	Invitees     []Invitee
}

// Invitee is one invited user as the inviter sees it.
type Invitee struct {
	DisplayName string
	JoinedAt    time.Time
	RebateTotal money.Amount
}

const recentInvitees = 50

// ReferralInfo returns userID's invite code and referral statistics.
func (s *Service) ReferralInfo(ctx context.Context, userID uuid.UUID) (*ReferralInfo, error) {
	code, err := s.InviteCode(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := &ReferralInfo{Config: s.referralConfig(ctx), Code: code, Invitees: []Invitee{}}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM referrals WHERE inviter_id = $1`, userID).
		Scan(&out.InvitedCount); err != nil {
		return nil, err
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(rebate_nano), 0)::bigint FROM referral_rebates WHERE inviter_id = $1`,
		userID).Scan(&total); err != nil {
		return nil, err
	}
	out.RebateTotal = money.Amount(total)
	rows, err := s.pool.Query(ctx, `SELECT u.display_name, r.created_at,
			COALESCE((SELECT sum(b.rebate_nano) FROM referral_rebates b WHERE b.inviter_id = r.inviter_id
				AND b.invitee_id = r.invitee_id), 0)::bigint
		FROM referrals r JOIN users u ON u.id = r.invitee_id
		WHERE r.inviter_id = $1 ORDER BY r.created_at DESC, r.invitee_id DESC LIMIT $2`, userID, recentInvitees)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v Invitee
		var sum int64
		if err := rows.Scan(&v.DisplayName, &v.JoinedAt, &sum); err != nil {
			return nil, err
		}
		v.DisplayName, v.JoinedAt, v.RebateTotal = MaskName(v.DisplayName), v.JoinedAt.UTC(), money.Amount(sum)
		out.Invitees = append(out.Invitees, v)
	}
	return out, rows.Err()
}

// ReferralRebate is one row of GET /api/billing/referral/rebates.
type ReferralRebate struct {
	ID          uuid.UUID
	InviteeName string
	Recharge    money.Amount
	Rate        string
	Rebate      money.Amount
	CreatedAt   time.Time
}

// ListReferralRebates returns the rebates paid to userID newest first.
func (s *Service) ListReferralRebates(ctx context.Context, userID uuid.UUID, offset, limit int) ([]ReferralRebate, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM referral_rebates WHERE inviter_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT b.id, u.display_name, b.recharge_nano, b.rate, b.rebate_nano, b.created_at
		FROM referral_rebates b JOIN users u ON u.id = b.invitee_id
		WHERE b.inviter_id = $1 ORDER BY b.created_at DESC, b.id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ReferralRebate{}
	for rows.Next() {
		var r ReferralRebate
		var recharge, rebate int64
		if err := rows.Scan(&r.ID, &r.InviteeName, &recharge, &r.Rate, &rebate, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		r.InviteeName, r.Recharge, r.Rebate, r.CreatedAt = MaskName(r.InviteeName), money.Amount(recharge), money.Amount(rebate), r.CreatedAt.UTC()
		out = append(out, r)
	}
	return out, total, rows.Err()
}
