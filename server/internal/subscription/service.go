// Package subscription implements plans, subscriptions and periodic quotas
// (ADR-0006, contracts/phase3-api.md §1–§5).
//
// A subscription snapshots its plan's models and rules at grant time. Quota
// counters live in quota_usage and are read on every gateway request (Decide)
// and incremented in the settlement transaction (Record), so several instances
// share the same semantics without an in-process cache.
//
// Lock order: per-user advisory lock (LockUser) → plans → subscriptions. Record
// only locks its subscription row, which also serializes session starts.
package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

// Audit actions written by this package.
const (
	ActionPlanCreate = "plan.create"
	ActionPlanUpdate = "plan.update"
	ActionGrant      = "subscription.grant"
	ActionRenew      = "subscription.renew"
	ActionCancel     = "subscription.cancel"
	ActionPrefs      = "billing.preferences"
)

// Plan and subscription states and sources.
const (
	PlanActive   = "active"
	PlanArchived = "archived"

	StatusActive    = "active"
	StatusExpired   = "expired" // derived: now >= ends_at
	StatusCancelled = "cancelled"

	SourceAdmin  = "admin"
	SourceRedeem = "redeem"

	MaxPeriods     = 120
	maxNameLen     = 100
	maxDescLen     = 2000
	maxNoteLen     = 500
	mySubsLimit    = 50
	userLockDomain = 0x7375_6273 // "subs": advisory lock class for LockUser
)

// Service owns plans, subscriptions and quota accounting.
type Service struct {
	pool *db.DB
	rec  *audit.Recorder
	log  *slog.Logger
	now  func() time.Time

	// OnQuota, when set, receives the state of every rule that reached 80 %
	// of its limit after a committed Record (notifications).
	OnQuota func(ctx context.Context, q QuotaState)
	// OnBulk, when set, receives every committed quota reset or extension
	// (notifications; phase7-api.md §3).
	OnBulk func(ctx context.Context, n BulkNotice)
	// OnCards, when set, receives every committed reset card batch
	// (notifications; phase11-api.md §2.1).
	OnCards func(ctx context.Context, n CardNotice)
	// CustomMeters evaluates billing-plugin meters (nil: custom meters are
	// rejected on save and count 0).
	CustomMeters CustomMeters
}

// QuotaState is a rule's usage in its current window after a Record.
type QuotaState struct {
	UserID         uuid.UUID
	SubscriptionID uuid.UUID
	PlanName       string
	RuleID         string
	RuleLabel      string
	Used           string
	Limit          string
	Exhausted      bool // used >= limit
	// WindowKey identifies the window ("once per window"): its start for
	// calendar/period/session/lifetime windows, a duration-sized bucket for
	// rolling ones.
	WindowKey string
	ResetsAt  *time.Time
}

// NewService creates the subscription service.
func NewService(pool *db.DB, rec *audit.Recorder, log *slog.Logger) *Service {
	return &Service{pool: pool, rec: rec, log: log, now: func() time.Time { return time.Now().UTC() }}
}

// Actor identifies who performs an audited operation.
type Actor struct {
	ID   uuid.UUID
	Name string
}

// RequestMeta carries request context for audit entries.
type RequestMeta struct {
	IPPrefix  string
	RequestID string
}

// PlanArchivedError is returned when granting an archived plan.
func PlanArchivedError() *apperr.Error {
	return apperr.New(apperr.KindConflict, "plan_archived", "套餐已下架，不能再开通")
}

// NotActiveError is returned when cancelling a subscription that is not active.
func NotActiveError() *apperr.Error {
	return apperr.New(apperr.KindConflict, "subscription_not_active", "订阅不是有效状态")
}

// dbTime truncates to PostgreSQL's microsecond precision so values written and
// read back compare equal (SQLite keeps nanoseconds; truncating there too keeps
// both backends identical).
func dbTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// ---- plans ----

// Plan is a sellable set of quota rules.
type Plan struct {
	ID          uuid.UUID
	Name        string
	Description string
	ListPrice   *money.Amount
	Duration    string
	Models      []string
	Rules       []Rule
	Stackable   bool
	Status      string
	Subscribers int
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PlanInput is the full editable state of a plan.
type PlanInput struct {
	Name        string
	Description string
	ListPrice   *money.Amount
	Duration    string
	Models      []string
	Rules       []Rule
	Stackable   bool
	Status      string // "" → active
}

// PlanPatch changes selected fields; nil fields are kept. ListPrice is applied
// when SetListPrice is true (nil clears it).
type PlanPatch struct {
	Name         *string
	Description  *string
	SetListPrice bool
	ListPrice    *money.Amount
	Duration     *string
	Models       *[]string
	Rules        *[]Rule
	Stackable    *bool
	Status       *string
	Version      int
}

func validatePlan(in *PlanInput) error {
	details := map[string]any{}
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Status == "" {
		in.Status = PlanActive
	}
	if in.Models == nil {
		in.Models = []string{}
	}
	switch n := utf8.RuneCountInString(in.Name); {
	case n == 0:
		details["name"] = "必填"
	case n > maxNameLen:
		details["name"] = fmt.Sprintf("不能超过 %d 个字符", maxNameLen)
	}
	if utf8.RuneCountInString(in.Description) > maxDescLen {
		details["description"] = fmt.Sprintf("不能超过 %d 个字符", maxDescLen)
	}
	if in.ListPrice != nil && *in.ListPrice < 0 {
		details["listPrice"] = "不能为负数"
	}
	if in.Duration == "" {
		details["duration"] = "必填"
	} else if _, err := ParsePeriod(in.Duration); err != nil {
		details["duration"] = err.Error()
	}
	if msg := validateModels(in.Models); msg != "" {
		details["models"] = msg
	}
	validateRules(in.Rules, details)
	if in.Status != PlanActive && in.Status != PlanArchived {
		details["status"] = "必须为 active 或 archived"
	}
	if len(details) > 0 {
		return apperr.Validation("参数校验失败", details)
	}
	return nil
}

// planCols needs $1 = now (for the live subscriber count).
const planCols = `p.id, p.name, p.description, p.list_price_nano, p.duration, p.models, p.rules, p.stackable, p.status,
	(SELECT count(*) FROM subscriptions s WHERE s.plan_id = p.id AND s.status = 'active' AND s.ends_at > $1),
	p.version, p.created_at, p.updated_at`

func scanPlan(row db.Row) (*Plan, error) {
	var p Plan
	var price *int64
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &price, &p.Duration, &p.Models, &p.Rules, &p.Stackable,
		&p.Status, &p.Subscribers, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if price != nil {
		p.ListPrice = ptr(money.Amount(*price))
	}
	for i := range p.Rules {
		p.Rules[i].normalize()
	}
	if p.Models == nil {
		p.Models = []string{}
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return &p, nil
}

func priceNano(p *money.Amount) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}

func planAudit(p *Plan) map[string]any {
	var price *string
	if p.ListPrice != nil {
		price = ptr(p.ListPrice.String())
	}
	return map[string]any{"name": p.Name, "description": p.Description, "listPrice": price, "duration": p.Duration,
		"models": p.Models, "rules": p.Rules, "stackable": p.Stackable, "status": p.Status}
}

// GetPlan returns a plan by id.
func (s *Service) GetPlan(ctx context.Context, id uuid.UUID) (*Plan, error) {
	p, err := scanPlan(s.pool.QueryRow(ctx, `SELECT `+planCols+` FROM plans p WHERE p.id = $2`, s.now(), id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("套餐")
	}
	return p, err
}

// ListPlans returns plans newest first; status filters when non-empty.
func (s *Service) ListPlans(ctx context.Context, status string, offset, limit int) ([]*Plan, int, error) {
	where, args := "", []any{s.now()}
	if status != "" {
		args = append(args, status)
		where = " WHERE p.status = $2"
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM plans p`+strings.ReplaceAll(where, "$2", "$1"),
		args[1:]...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	n := len(args)
	rows, err := s.pool.Query(ctx, `SELECT `+planCols+` FROM plans p`+where+
		` ORDER BY p.created_at DESC, p.id DESC LIMIT $`+strconv.Itoa(n-1)+` OFFSET $`+strconv.Itoa(n), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// ValidatePlan validates and normalizes in exactly like CreatePlan (custom
// meters are resolved and pinned) without writing; rules are copied, in is
// not modified.
func (s *Service) ValidatePlan(ctx context.Context, in PlanInput) (PlanInput, error) {
	in.Rules = append([]Rule(nil), in.Rules...)
	in.Models = append([]string(nil), in.Models...)
	if err := validatePlan(&in); err != nil {
		return in, err
	}
	details := map[string]any{}
	if err := s.resolveCustomMeters(ctx, in.Rules, details); err != nil {
		return in, err
	}
	if len(details) > 0 {
		return in, apperr.Validation("参数校验失败", details)
	}
	return in, nil
}

// CreatePlan validates and stores a new plan. The nil actor ID is the system
// actor (`omnigate seed`): the plan then has no creator.
func (s *Service) CreatePlan(ctx context.Context, actor Actor, in PlanInput, meta RequestMeta) (*Plan, error) {
	in, err := s.ValidatePlan(ctx, in)
	if err != nil {
		return nil, err
	}
	var createdBy *uuid.UUID
	if actor.ID != uuid.Nil {
		createdBy = &actor.ID
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	models, _ := json.Marshal(in.Models)
	rules, _ := json.Marshal(in.Rules)
	now := s.now()
	var p *Plan
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO plans (id, name, description, list_price_nano, duration, models, rules,
				stackable, status, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)`,
			id, in.Name, in.Description, priceNano(in.ListPrice), in.Duration, models, rules, in.Stackable, in.Status,
			createdBy, now); err != nil {
			return err
		}
		var err error
		if p, err = scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM plans p WHERE p.id = $2`, now, id)); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, ActionPlanCreate, "plan", id.String(), planAudit(p))
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// UpdatePlan applies patch with optimistic locking. Existing subscriptions keep
// their snapshots.
func (s *Service) UpdatePlan(ctx context.Context, actor Actor, id uuid.UUID, patch PlanPatch, meta RequestMeta) (*Plan, error) {
	now := s.now()
	var p *Plan
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		cur, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM plans p WHERE p.id = $2 FOR UPDATE OF p`, now, id))
		if db.IsNoRows(err) {
			return apperr.NotFound("套餐")
		}
		if err != nil {
			return err
		}
		if cur.Version != patch.Version {
			return apperr.VersionConflict()
		}
		in := PlanInput{Name: cur.Name, Description: cur.Description, ListPrice: cur.ListPrice, Duration: cur.Duration,
			Models: cur.Models, Rules: cur.Rules, Stackable: cur.Stackable, Status: cur.Status}
		if patch.Name != nil {
			in.Name = *patch.Name
		}
		if patch.Description != nil {
			in.Description = *patch.Description
		}
		if patch.SetListPrice {
			in.ListPrice = patch.ListPrice
		}
		if patch.Duration != nil {
			in.Duration = *patch.Duration
		}
		if patch.Models != nil {
			in.Models = *patch.Models
		}
		if patch.Rules != nil {
			in.Rules = *patch.Rules
		}
		if patch.Stackable != nil {
			in.Stackable = *patch.Stackable
		}
		if patch.Status != nil {
			in.Status = *patch.Status
		}
		if err := validatePlan(&in); err != nil {
			return err
		}
		if patch.Rules != nil {
			// Saved rules re-resolve their custom meters to the newest
			// approved plugin version; otherwise the stored pins are kept.
			details := map[string]any{}
			if err := s.resolveCustomMeters(ctx, in.Rules, details); err != nil {
				return err
			}
			if len(details) > 0 {
				return apperr.Validation("参数校验失败", details)
			}
		}
		models, _ := json.Marshal(in.Models)
		rules, _ := json.Marshal(in.Rules)
		if _, err := tx.Exec(ctx, `UPDATE plans SET name = $2, description = $3, list_price_nano = $4, duration = $5,
				models = $6, rules = $7, stackable = $8, status = $9, version = version + 1, updated_at = $10
			WHERE id = $1`, id, in.Name, in.Description, priceNano(in.ListPrice), in.Duration, models, rules,
			in.Stackable, in.Status, now); err != nil {
			return err
		}
		if p, err = scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM plans p WHERE p.id = $2`, now, id)); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, ActionPlanUpdate, "plan", id.String(), map[string]any{
			"before": planAudit(cur), "after": planAudit(p),
		})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ---- subscriptions ----

// Subscription is a user's plan instance with snapshotted models and rules.
type Subscription struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	UserName    string
	PlanID      uuid.UUID
	PlanName    string
	Models      []string
	Rules       []Rule
	StartsAt    time.Time
	EndsAt      time.Time
	State       string // stored: active | cancelled
	Source      string
	SourceRef   *string
	CancelledAt *time.Time
	CancelNote  *string
	CreatedAt   time.Time
	Version     int
}

// StatusAt derives the API status (active | expired | cancelled) at now.
func (s *Subscription) StatusAt(now time.Time) string {
	switch {
	case s.State == StatusCancelled:
		return StatusCancelled
	case !now.Before(s.EndsAt):
		return StatusExpired
	default:
		return StatusActive
	}
}

const subCols = `s.id, s.user_id, u.display_name, s.plan_id, s.plan_name, s.models, s.rules, s.starts_at, s.ends_at,
	s.status, s.source, s.source_ref, s.cancelled_at, s.cancel_note, s.created_at, s.version`

const subFrom = ` FROM subscriptions s JOIN users u ON u.id = s.user_id`

func scanSub(row db.Row) (*Subscription, error) {
	var s Subscription
	if err := row.Scan(&s.ID, &s.UserID, &s.UserName, &s.PlanID, &s.PlanName, &s.Models, &s.Rules, &s.StartsAt,
		&s.EndsAt, &s.State, &s.Source, &s.SourceRef, &s.CancelledAt, &s.CancelNote, &s.CreatedAt, &s.Version); err != nil {
		return nil, err
	}
	if s.Models == nil {
		s.Models = []string{}
	}
	for i := range s.Rules {
		s.Rules[i].normalize()
	}
	s.StartsAt, s.EndsAt, s.CreatedAt = s.StartsAt.UTC(), s.EndsAt.UTC(), s.CreatedAt.UTC()
	if s.CancelledAt != nil {
		s.CancelledAt = ptr(s.CancelledAt.UTC())
	}
	return &s, nil
}

func collectSubs(rows db.Rows) ([]*Subscription, error) {
	defer rows.Close()
	out := []*Subscription{}
	for rows.Next() {
		s, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func getSub(ctx context.Context, q db.Querier, id uuid.UUID, lock bool) (*Subscription, error) {
	sql := `SELECT ` + subCols + subFrom + ` WHERE s.id = $1`
	if lock {
		sql += ` FOR UPDATE OF s`
	}
	s, err := scanSub(q.QueryRow(ctx, sql, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("订阅")
	}
	return s, err
}

// LockUser takes a transaction-scoped advisory lock serializing grants (and plan
// redemptions) of one user. q must be a transaction. On SQLite the transaction
// already holds the database write lock, so there is nothing to take.
func LockUser(ctx context.Context, q db.Querier, userID uuid.UUID) error {
	if q.Dialect() == db.SQLite {
		return nil
	}
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, int32(userLockDomain), userID.String())
	return err
}

// GrantTx grants periods × plan.duration of planID to userID inside the caller's
// transaction q: a non-stackable plan extends the user's live subscription of
// the same plan (renewed = true, snapshot unchanged); otherwise a new
// subscription starting at now snapshots the plan. The plan must be active.
// It does not write audit entries (callers audit their own action).
func GrantTx(ctx context.Context, q db.Querier, now time.Time, userID, planID uuid.UUID, periods int,
	source, sourceRef string) (sub *Subscription, renewed bool, err error) {
	if periods < 1 || periods > MaxPeriods {
		return nil, false, apperr.Validation("参数校验失败", map[string]any{
			"periods": fmt.Sprintf("必须在 1 到 %d 之间", MaxPeriods)})
	}
	if source != SourceAdmin && source != SourceRedeem {
		return nil, false, fmt.Errorf("subscription: invalid source %q", source)
	}
	now = dbTime(now)
	if err := LockUser(ctx, q, userID); err != nil {
		return nil, false, err
	}
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, userID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, apperr.NotFound("用户")
	}
	var name, duration, status string
	var stackable bool
	var models, rules []byte
	err = q.QueryRow(ctx, `SELECT name, duration, models, rules, stackable, status FROM plans WHERE id = $1 FOR SHARE`,
		planID).Scan(&name, &duration, &models, &rules, &stackable, &status)
	if db.IsNoRows(err) {
		return nil, false, apperr.NotFound("套餐")
	}
	if err != nil {
		return nil, false, err
	}
	if status != PlanActive {
		return nil, false, PlanArchivedError()
	}
	d, err := ParsePeriod(duration)
	if err != nil {
		return nil, false, fmt.Errorf("subscription: plan %s has invalid duration %q", planID, duration)
	}
	ext := d * time.Duration(periods)
	var ref *string
	if sourceRef != "" {
		ref = &sourceRef
	}
	if !stackable {
		var id uuid.UUID
		var ends time.Time
		err := q.QueryRow(ctx, `SELECT id, ends_at FROM subscriptions
			WHERE user_id = $1 AND plan_id = $2 AND status = 'active' AND ends_at > $3
			ORDER BY ends_at DESC, id DESC LIMIT 1 FOR UPDATE`, userID, planID, now).Scan(&id, &ends)
		switch {
		case err == nil:
			if _, err := q.Exec(ctx, `UPDATE subscriptions SET ends_at = $2, version = version + 1, updated_at = $3
				WHERE id = $1`, id, ends.Add(ext), now); err != nil {
				return nil, false, err
			}
			sub, err := getSub(ctx, q, id, false)
			return sub, true, err
		case !db.IsNoRows(err):
			return nil, false, err
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, false, err
	}
	if _, err := q.Exec(ctx, `INSERT INTO subscriptions (id, user_id, plan_id, plan_name, models, rules, starts_at,
			ends_at, source, source_ref, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $7, $7)`,
		id, userID, planID, name, models, rules, now, now.Add(ext), source, ref); err != nil {
		return nil, false, err
	}
	sub, err = getSub(ctx, q, id, false)
	return sub, false, err
}

// Grant is GrantTx at the service clock.
func (s *Service) Grant(ctx context.Context, q db.Querier, userID, planID uuid.UUID, periods int, source, sourceRef string) (*Subscription, bool, error) {
	return GrantTx(ctx, q, s.now(), userID, planID, periods, source, sourceRef)
}

// AdminGrant grants or renews a plan for a user (POST /admin/billing/subscriptions).
func (s *Service) AdminGrant(ctx context.Context, actor Actor, userID, planID uuid.UUID, periods int, meta RequestMeta) (*SubscriptionView, bool, error) {
	now := s.now()
	var view *SubscriptionView
	var renewed bool
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		sub, r, err := GrantTx(ctx, tx, now, userID, planID, periods, SourceAdmin, actor.ID.String())
		if err != nil {
			return err
		}
		renewed = r
		action := ActionGrant
		if renewed {
			action = ActionRenew
		}
		if view, err = DescribeTx(ctx, tx, sub, now); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, action, "subscription", sub.ID.String(), map[string]any{
			"userId": userID, "planId": planID, "planName": sub.PlanName, "periods": periods,
			"startsAt": sub.StartsAt, "endsAt": sub.EndsAt,
		})
	})
	if err != nil {
		return nil, false, err
	}
	return view, renewed, nil
}

// Cancel ends an active subscription immediately.
func (s *Service) Cancel(ctx context.Context, actor Actor, id uuid.UUID, note string, meta RequestMeta) (*SubscriptionView, error) {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > maxNoteLen {
		return nil, apperr.Validation("参数校验失败", map[string]any{"note": fmt.Sprintf("不能超过 %d 个字符", maxNoteLen)})
	}
	now := dbTime(s.now())
	var view *SubscriptionView
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		sub, err := getSub(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if sub.StatusAt(now) != StatusActive {
			return NotActiveError()
		}
		var notePtr *string
		if note != "" {
			notePtr = &note
		}
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET status = 'cancelled', cancelled_at = $2, cancelled_by = $3,
				cancel_note = $4, version = version + 1, updated_at = $2 WHERE id = $1`,
			id, now, actor.ID, notePtr); err != nil {
			return err
		}
		if sub, err = getSub(ctx, tx, id, false); err != nil {
			return err
		}
		if view, err = DescribeTx(ctx, tx, sub, now); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, meta, ActionCancel, "subscription", id.String(), map[string]any{
			"userId": sub.UserID, "planId": sub.PlanID, "planName": sub.PlanName, "endsAt": sub.EndsAt, "note": note,
		})
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// ListMine returns the user's most recent subscriptions (including expired and
// cancelled ones) with current usage.
func (s *Service) ListMine(ctx context.Context, userID uuid.UUID) ([]*SubscriptionView, error) {
	return s.Recent(ctx, userID, mySubsLimit)
}

// Recent returns the user's newest limit subscriptions (any status) with
// current usage.
func (s *Service) Recent(ctx context.Context, userID uuid.UUID, limit int) ([]*SubscriptionView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+subCols+subFrom+` WHERE s.user_id = $1
		ORDER BY s.created_at DESC, s.id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	subs, err := collectSubs(rows)
	if err != nil {
		return nil, err
	}
	return describe(ctx, s.pool, subs, s.now())
}

// SubscriptionFilter narrows the admin subscription list.
type SubscriptionFilter struct {
	UserID *uuid.UUID
	PlanID *uuid.UUID
	Status string // active | expired | cancelled | ""
}

// ListSubscriptions returns subscriptions newest first with current usage.
func (s *Service) ListSubscriptions(ctx context.Context, f SubscriptionFilter, offset, limit int) ([]*SubscriptionView, int, error) {
	now := s.now()
	conds, args := []string{"true"}, []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args))))
	}
	if f.UserID != nil {
		add("s.user_id = $?", *f.UserID)
	}
	if f.PlanID != nil {
		add("s.plan_id = $?", *f.PlanID)
	}
	switch f.Status {
	case "":
	case StatusActive:
		add("s.status = 'active' AND s.ends_at > $?", now)
	case StatusExpired:
		add("s.status = 'active' AND s.ends_at <= $?", now)
	case StatusCancelled:
		conds = append(conds, "s.status = 'cancelled'")
	default:
		return nil, 0, apperr.Validation("参数校验失败", map[string]any{"status": "必须为 active、expired 或 cancelled"})
	}
	where := " WHERE " + strings.Join(conds, " AND ")
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM subscriptions s`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	n := len(args)
	rows, err := s.pool.Query(ctx, `SELECT `+subCols+subFrom+where+` ORDER BY s.created_at DESC, s.id DESC
		LIMIT $`+strconv.Itoa(n-1)+` OFFSET $`+strconv.Itoa(n), args...)
	if err != nil {
		return nil, 0, err
	}
	subs, err := collectSubs(rows)
	if err != nil {
		return nil, 0, err
	}
	views, err := describe(ctx, s.pool, subs, now)
	return views, total, err
}

func (s *Service) audit(ctx context.Context, q db.Querier, actor Actor, meta RequestMeta, action, resType, resID string, md map[string]any) error {
	id, name := actor.ID, actor.Name
	return s.rec.Record(ctx, q, audit.Entry{
		ActorID: &id, ActorName: &name, Action: action, ResourceType: resType, ResourceID: &resID,
		IPPrefix: meta.IPPrefix, RequestID: meta.RequestID, Metadata: md,
	})
}

// ---- model plaza (docs/contracts/phase5-api.md §3) ----

// PlanCoverage is an active plan and the models it covers (empty = all).
type PlanCoverage struct {
	ID     uuid.UUID
	Name   string
	Models []string
}

// Covers reports whether the plan covers model.
func (p PlanCoverage) Covers(model string) bool { return covers(p.Models, model) }

// ActivePlans returns every active plan, oldest first.
func (s *Service) ActivePlans(ctx context.Context) ([]PlanCoverage, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, models FROM plans WHERE status = $1 ORDER BY created_at, id`, PlanActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlanCoverage{}
	for rows.Next() {
		var p PlanCoverage
		if err := rows.Scan(&p.ID, &p.Name, &p.Models); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LiveSubscriptions returns the user's subscriptions valid at now, the one
// expiring first first (the order Decide uses).
func (s *Service) LiveSubscriptions(ctx context.Context, userID uuid.UUID, now time.Time) ([]*Subscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+subCols+subFrom+`
		WHERE s.user_id = $1 AND s.status = 'active' AND s.ends_at > $2 AND s.starts_at <= $2
		ORDER BY s.ends_at, s.id`, userID, now)
	if err != nil {
		return nil, err
	}
	return collectSubs(rows)
}

// Covers reports whether the subscription's snapshot covers model.
func (s *Subscription) Covers(model string) bool { return covers(s.Models, model) }
