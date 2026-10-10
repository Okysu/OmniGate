package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/platform/db"
)

// Quota reset cards (docs/contracts/phase11-api.md §2): an administrator
// issues a batch of cards to a set of users, materialized at issue time (one
// row per card); a user spends a card on one of their live subscriptions to
// reset its 5-hour and / or weekly windows with the anchored reset of
// resetRules. A card matches rules by window length, not by rule id.

// Card kinds.
const (
	CardKind5h     = "5h"
	CardKindWeekly = "weekly"
	CardKindBoth   = "both"
)

// Card states. CardExpired is derived: stored 'available' and expiresAt <= now.
const (
	CardAvailable = "available"
	CardUsed      = "used"
	CardRevoked   = "revoked"
	CardExpired   = "expired"

	CardBatchActive  = "active"
	CardBatchRevoked = "revoked"
)

// Card targets: who receives the cards of a batch.
const (
	CardTargetUsers = "users" // explicit user ids
	CardTargetGroup = "group" // members of a user group
	CardTargetPlan  = "plan"  // holders of an active subscription of a plan
	CardTargetAll   = "all"   // every user
)

// Audit actions of reset cards.
const (
	ActionCardIssue  = "reset_card.issue"
	ActionCardRevoke = "reset_card.revoke"
	ActionCardUse    = "reset_card.use"
)

const (
	maxCardQuantity = 100     // cards per recipient
	maxCardTotal    = 200_000 // cards per batch
	maxCardPlans    = 50
	maxMyCards      = 500
	cardNoticeChunk = 500 // recipients per notification event
	window5h        = 5 * time.Hour
	windowWeek      = 7 * 24 * time.Hour
)

// ValidCardKind reports whether k is a card kind.
func ValidCardKind(k string) bool {
	return k == CardKind5h || k == CardKindWeekly || k == CardKindBoth
}

// CardKindLabel is the Chinese name of a card kind.
func CardKindLabel(k string) string {
	switch k {
	case CardKind5h:
		return "5小时重置卡"
	case CardKindWeekly:
		return "周重置卡"
	case CardKindBoth:
		return "双重置卡"
	}
	return k
}

// cardMatches reports whether a card of kind resets r: session and rolling
// windows of exactly 5 hours (5h), 7 days (weekly) or either (both).
func cardMatches(kind string, r *compiledRule) bool {
	if r.Window.Kind != WindowSession && r.Window.Kind != WindowRolling {
		return false
	}
	switch kind {
	case CardKind5h:
		return r.dur == window5h
	case CardKindWeekly:
		return r.dur == windowWeek
	case CardKindBoth:
		return r.dur == window5h || r.dur == windowWeek
	}
	return false
}

// cardRules returns the rules of ls a card of kind resets.
func cardRules(kind string, ls *loadedSub) []*compiledRule {
	var out []*compiledRule
	for _, r := range ls.rules {
		if cardMatches(kind, r) {
			out = append(out, r)
		}
	}
	return out
}

// ---- errors ----

func cardConflict(code, msg string) *apperr.Error { return apperr.New(apperr.KindConflict, code, msg) }

// CardNotApplicableError is returned when a card resets no rule of the
// subscription (the card is not consumed).
func CardNotApplicableError(kind string) *apperr.Error {
	what := map[string]string{CardKind5h: "5 小时", CardKindWeekly: "每周", CardKindBoth: "5 小时或每周"}[kind]
	return apperr.New(apperr.KindValidation, "card_not_applicable", "该订阅没有可用此卡重置的"+what+"额度")
}

// CardPlanError is returned when the card is restricted to other plans.
func CardPlanError() *apperr.Error {
	return apperr.New(apperr.KindValidation, "card_plan_not_allowed", "该重置卡不能用于此套餐")
}

// ---- views ----

// CardTarget selects the recipients of a batch. Name snapshots (GroupName,
// PlanName) are filled when the batch is issued.
type CardTarget struct {
	Type      string      `json:"type"`
	UserIDs   []uuid.UUID `json:"userIds,omitempty"`
	GroupID   *uuid.UUID  `json:"groupId,omitempty"`
	GroupName string      `json:"groupName,omitempty"`
	PlanID    *uuid.UUID  `json:"planId,omitempty"`
	PlanName  string      `json:"planName,omitempty"`
}

// CardCounts are the cards of a batch by (derived) state.
type CardCounts struct {
	Issued    int `json:"issued"`
	Used      int `json:"used"`
	Available int `json:"available"`
	Expired   int `json:"expired"`
	Revoked   int `json:"revoked"`
}

// CardBatch is the API form of a batch (§2.1).
type CardBatch struct {
	ID         uuid.UUID  `json:"id"`
	Kind       string     `json:"kind"`
	Quantity   int        `json:"quantity"`
	Recipients int        `json:"recipients"`
	Target     CardTarget `json:"target"`
	Plans      []IDName   `json:"plans"` // [] = every plan
	ExpiresAt  *time.Time `json:"expiresAt"`
	Note       string     `json:"note"`
	Status     string     `json:"status"`
	Counts     CardCounts `json:"counts"`
	CreatedBy  UserRef    `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt"`

	planIDs []uuid.UUID
}

// CardSubRef is the subscription a card was used on.
type CardSubRef struct {
	ID       uuid.UUID `json:"id"`
	PlanName string    `json:"planName"`
}

// Card is the API form of a reset card (§2.2).
type Card struct {
	ID           uuid.UUID   `json:"id"`
	BatchID      uuid.UUID   `json:"batchId"`
	User         UserRef     `json:"user"`
	Kind         string      `json:"kind"`
	Status       string      `json:"status"`
	ExpiresAt    *time.Time  `json:"expiresAt"`
	Plans        []IDName    `json:"plans"` // [] = every plan
	Note         string      `json:"note"`
	CreatedAt    time.Time   `json:"createdAt"`
	UsedAt       *time.Time  `json:"usedAt"`
	Subscription *CardSubRef `json:"subscription"`
	RevokedAt    *time.Time  `json:"revokedAt"`

	planIDs []uuid.UUID
}

// cardStatus derives the API status of a stored card at now.
func cardStatus(stored string, expiresAt *time.Time, now time.Time) string {
	if stored == CardAvailable && expiresAt != nil && !now.Before(*expiresAt) {
		return CardExpired
	}
	return stored
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return ptr(t.UTC())
}

// decodePlanIDs parses a nullable plan_ids document.
func decodePlanIDs(raw []byte) ([]uuid.UUID, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var ids []uuid.UUID
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("subscription: bad plan_ids %q: %w", raw, err)
	}
	return ids, nil
}

// planNames resolves plan ids to names in one query (missing plans keep
// their id as the name).
func planNames(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name FROM plans WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func planRefs(ids []uuid.UUID, names map[uuid.UUID]string) []IDName {
	out := make([]IDName, len(ids))
	for i, id := range ids {
		name, ok := names[id]
		if !ok {
			name = id.String()
		}
		out[i] = IDName{ID: id, Name: name}
	}
	return out
}

// ---- issuing ----

// IssueCardsInput is the body of POST /admin/billing/reset-cards/batches.
type IssueCardsInput struct {
	Kind      string
	Quantity  int
	ExpiresAt *time.Time
	PlanIDs   []uuid.UUID // nil or empty = every plan
	Note      string
	Target    CardTarget
}

// IssueCardsResult is the response of an issue (also for dry runs, without
// Batch).
type IssueCardsResult struct {
	Recipients int        `json:"recipients"`
	Cards      int        `json:"cards"`
	Batch      *CardBatch `json:"batch"`
}

// CardNotice describes committed cards of one batch (notifications).
type CardNotice struct {
	BatchID   uuid.UUID
	Kind      string
	Quantity  int
	ExpiresAt *time.Time
	PlanNames []string // empty = every plan
	Note      string
	// Users are the recipients in chunks of at most cardNoticeChunk, so one
	// notification event never fans out to an unbounded number of users.
	Users [][]uuid.UUID
}

// rolesWith returns the roles granting perm.
func rolesWith(perm authz.Permission) []string {
	var out []string
	for _, r := range []identity.Role{identity.RoleSystemAdmin, identity.RoleChannelAdmin, identity.RoleUser, identity.RoleAuditor} {
		if slices.Contains(authz.Permissions(r), perm) {
			out = append(out, string(r))
		}
	}
	return out
}

// cardRecipients returns the users t selects at now: active users whose role
// may use cards (billing.own), ordered by id. It fills t's name snapshots and
// fails with 404 for an unknown group or plan.
func cardRecipients(ctx context.Context, q db.Querier, t *CardTarget, now time.Time) ([]uuid.UUID, error) {
	sql, args := `SELECT u.id FROM users u WHERE u.status = 'active' AND u.role = ANY($1)`, []any{rolesWith(authz.BillingOwn)}
	switch t.Type {
	case CardTargetUsers:
		args = append(args, t.UserIDs)
		sql += ` AND u.id = ANY($2)`
	case CardTargetGroup:
		err := q.QueryRow(ctx, `SELECT name FROM user_groups WHERE id = $1`, *t.GroupID).Scan(&t.GroupName)
		if db.IsNoRows(err) {
			return nil, apperr.NotFound("用户组")
		}
		if err != nil {
			return nil, err
		}
		args = append(args, *t.GroupID)
		sql += ` AND u.group_id = $2`
	case CardTargetPlan:
		err := q.QueryRow(ctx, `SELECT name FROM plans WHERE id = $1`, *t.PlanID).Scan(&t.PlanName)
		if db.IsNoRows(err) {
			return nil, apperr.NotFound("套餐")
		}
		if err != nil {
			return nil, err
		}
		args = append(args, *t.PlanID, now)
		sql += ` AND EXISTS (SELECT 1 FROM subscriptions s WHERE s.user_id = u.id AND s.plan_id = $2
			AND s.status = 'active' AND s.ends_at > $3)`
	}
	rows, err := q.Query(ctx, sql+` ORDER BY u.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func validateIssue(in *IssueCardsInput, now time.Time) error {
	details := map[string]any{}
	if !ValidCardKind(in.Kind) {
		details["kind"] = "必须为 5h、weekly 或 both"
	}
	if in.Quantity < 1 || in.Quantity > maxCardQuantity {
		details["quantity"] = fmt.Sprintf("必须在 1 到 %d 之间", maxCardQuantity)
	}
	if in.ExpiresAt != nil {
		in.ExpiresAt = ptr(dbTime(*in.ExpiresAt))
		if !in.ExpiresAt.After(now) {
			details["expiresAt"] = "必须晚于当前时间"
		}
	}
	if len(in.PlanIDs) > maxCardPlans {
		details["planIds"] = fmt.Sprintf("最多 %d 个套餐", maxCardPlans)
	}
	in.PlanIDs = dedupeUUIDs(in.PlanIDs)
	in.Note = validateNote(in.Note, details)
	t := &in.Target
	switch t.Type {
	case CardTargetUsers:
		t.UserIDs = dedupeUUIDs(t.UserIDs)
		if len(t.UserIDs) == 0 || len(t.UserIDs) > maxBulkIDs {
			details["target"] = fmt.Sprintf("userIds 必须有 1 到 %d 个用户 ID", maxBulkIDs)
		}
	case CardTargetGroup:
		if t.GroupID == nil {
			details["target"] = "groupId 必填"
		}
	case CardTargetPlan:
		if t.PlanID == nil {
			details["target"] = "planId 必填"
		}
	case CardTargetAll:
	default:
		details["target"] = "type 必须为 users、group、plan 或 all"
	}
	if len(details) > 0 {
		return apperr.Validation("参数校验失败", details)
	}
	return nil
}

func dedupeUUIDs(ids []uuid.UUID) []uuid.UUID {
	if len(ids) == 0 {
		return nil
	}
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// IssueCards issues quantity cards of a kind to every user the target selects
// (§2.1). Recipients are resolved at issue time ("all users" are the users
// existing now). A dry run validates everything and only counts. Every
// recipient is notified after the commit (OnCards).
func (s *Service) IssueCards(ctx context.Context, actor Actor, in IssueCardsInput, dryRun bool, meta RequestMeta) (*IssueCardsResult, error) {
	now := dbTime(s.now())
	if err := validateIssue(&in, now); err != nil {
		return nil, err
	}
	var res IssueCardsResult
	var users []uuid.UUID
	var names map[uuid.UUID]string
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		res, users = IssueCardsResult{}, nil
		var err error
		if names, err = planNames(ctx, tx, in.PlanIDs); err != nil {
			return err
		}
		for _, id := range in.PlanIDs {
			if _, ok := names[id]; !ok {
				return apperr.Validation("参数校验失败", map[string]any{"planIds": "套餐不存在：" + id.String()})
			}
		}
		if users, err = cardRecipients(ctx, tx, &in.Target, now); err != nil {
			return err
		}
		res.Recipients, res.Cards = len(users), len(users)*in.Quantity
		if res.Cards > maxCardTotal {
			return apperr.Validation("参数校验失败", map[string]any{
				"quantity": fmt.Sprintf("本次将发放 %d 张，超过单批上限 %d 张", res.Cards, maxCardTotal)})
		}
		if dryRun {
			return nil
		}
		if len(users) == 0 {
			return apperr.Validation("参数校验失败", map[string]any{"target": "没有符合条件的用户（仅发放给状态正常的用户）"})
		}
		batchID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		var planIDs []byte
		if len(in.PlanIDs) > 0 {
			planIDs, _ = json.Marshal(in.PlanIDs)
		}
		target, _ := json.Marshal(in.Target)
		if _, err := tx.Exec(ctx, `INSERT INTO reset_card_batches (id, kind, quantity, recipients, target, plan_ids,
				expires_at, note, created_by, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			batchID, in.Kind, in.Quantity, len(users), target, planIDs, in.ExpiresAt, in.Note, actor.ID, now); err != nil {
			return err
		}
		rows := make([][]any, 0, res.Cards)
		for _, uid := range users {
			for range in.Quantity {
				id, err := uuid.NewV7()
				if err != nil {
					return err
				}
				rows = append(rows, []any{id, batchID, uid, in.Kind, in.ExpiresAt, CardAvailable, now})
			}
		}
		if _, err := tx.CopyFrom(ctx, "reset_cards",
			[]string{"id", "batch_id", "user_id", "kind", "expires_at", "status", "created_at"}, rows); err != nil {
			return err
		}
		batches, err := s.loadBatches(ctx, tx, now, ` WHERE b.id = $2`, batchID)
		if err != nil {
			return err
		}
		res.Batch = batches[0]
		listed := users
		if len(listed) > maxBulkListed {
			listed = listed[:maxBulkListed]
		}
		return s.audit(ctx, tx, actor, meta, ActionCardIssue, "reset_card_batch", batchID.String(), map[string]any{
			"kind": in.Kind, "quantity": in.Quantity, "target": in.Target, "planIds": in.PlanIDs, "expiresAt": in.ExpiresAt,
			"note": in.Note, "recipients": len(users), "cards": res.Cards, "users": listed,
		})
	})
	if err != nil {
		return nil, err
	}
	if !dryRun && s.OnCards != nil {
		n := CardNotice{BatchID: res.Batch.ID, Kind: in.Kind, Quantity: in.Quantity, ExpiresAt: in.ExpiresAt, Note: in.Note}
		for _, p := range res.Batch.Plans {
			n.PlanNames = append(n.PlanNames, p.Name)
		}
		for chunk := range slices.Chunk(users, cardNoticeChunk) {
			n.Users = append(n.Users, chunk)
		}
		s.OnCards(ctx, n)
	}
	return &res, nil
}

// batchCols needs $1 = now (for the derived expired count).
const batchCols = `b.id, b.kind, b.quantity, b.recipients, b.target, b.plan_ids, b.expires_at, b.note, b.status,
	b.created_by, cu.display_name, b.created_at, b.revoked_at,
	(SELECT count(*) FROM reset_cards c WHERE c.batch_id = b.id),
	(SELECT count(*) FROM reset_cards c WHERE c.batch_id = b.id AND c.status = 'used'),
	(SELECT count(*) FROM reset_cards c WHERE c.batch_id = b.id AND c.status = 'revoked'),
	(SELECT count(*) FROM reset_cards c WHERE c.batch_id = b.id AND c.status = 'available'
		AND c.expires_at IS NOT NULL AND c.expires_at <= $1)`

// loadBatches reads batches matching where (which may use $2…) with counts
// and plan names.
func (s *Service) loadBatches(ctx context.Context, q db.Querier, now time.Time, where string, args ...any) ([]*CardBatch, error) {
	rows, err := q.Query(ctx, `SELECT `+batchCols+` FROM reset_card_batches b JOIN users cu ON cu.id = b.created_by`+where,
		append([]any{now}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CardBatch{}
	var allPlans []uuid.UUID
	for rows.Next() {
		var b CardBatch
		var target, plans []byte
		if err := rows.Scan(&b.ID, &b.Kind, &b.Quantity, &b.Recipients, &target, &plans, &b.ExpiresAt, &b.Note, &b.Status,
			&b.CreatedBy.ID, &b.CreatedBy.DisplayName, &b.CreatedAt, &b.RevokedAt,
			&b.Counts.Issued, &b.Counts.Used, &b.Counts.Revoked, &b.Counts.Expired); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(target, &b.Target); err != nil {
			return nil, fmt.Errorf("subscription: bad card target %q: %w", target, err)
		}
		if b.planIDs, err = decodePlanIDs(plans); err != nil {
			return nil, err
		}
		allPlans = append(allPlans, b.planIDs...)
		b.Counts.Available = b.Counts.Issued - b.Counts.Used - b.Counts.Revoked - b.Counts.Expired
		b.ExpiresAt, b.RevokedAt, b.CreatedAt = utcPtr(b.ExpiresAt), utcPtr(b.RevokedAt), b.CreatedAt.UTC()
		out = append(out, &b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	names, err := planNames(ctx, q, dedupeUUIDs(allPlans))
	if err != nil {
		return nil, err
	}
	for _, b := range out {
		b.Plans = planRefs(b.planIDs, names)
	}
	return out, nil
}

// ListCardBatches returns batches newest first with their card counts.
func (s *Service) ListCardBatches(ctx context.Context, offset, limit int) ([]*CardBatch, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM reset_card_batches`).Scan(&total); err != nil {
		return nil, 0, err
	}
	items, err := s.loadBatches(ctx, s.pool, dbTime(s.now()), ` ORDER BY b.created_at DESC, b.id DESC LIMIT $2 OFFSET $3`, limit, offset)
	return items, total, err
}

// RevokeCardBatch revokes every unused card of a batch (§2.1); used cards
// stay used. Revoking a revoked batch is a conflict.
func (s *Service) RevokeCardBatch(ctx context.Context, actor Actor, id uuid.UUID, meta RequestMeta) (*CardBatch, error) {
	now := dbTime(s.now())
	var b *CardBatch
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM reset_card_batches WHERE id = $1 FOR UPDATE`, id).Scan(&status)
		if db.IsNoRows(err) {
			return apperr.NotFound("重置卡批次")
		}
		if err != nil {
			return err
		}
		if status == CardBatchRevoked {
			return cardConflict("card_batch_revoked", "该批次已作废")
		}
		if _, err := tx.Exec(ctx, `UPDATE reset_card_batches SET status = 'revoked', revoked_at = $2, revoked_by = $3 WHERE id = $1`,
			id, now, actor.ID); err != nil {
			return err
		}
		// Cards being used concurrently hold their row lock: this waits for them
		// and then skips them (status is no longer 'available').
		tag, err := tx.Exec(ctx, `UPDATE reset_cards SET status = 'revoked', revoked_at = $2 WHERE batch_id = $1 AND status = 'available'`,
			id, now)
		if err != nil {
			return err
		}
		batches, err := s.loadBatches(ctx, tx, now, ` WHERE b.id = $2`, id)
		if err != nil {
			return err
		}
		b = batches[0]
		return s.audit(ctx, tx, actor, meta, ActionCardRevoke, "reset_card_batch", id.String(), map[string]any{
			"kind": b.Kind, "revoked": tag.RowsAffected(), "used": b.Counts.Used, "note": b.Note,
		})
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ---- cards ----

const cardCols = `c.id, c.batch_id, c.user_id, u.display_name, c.kind, c.status, c.expires_at, b.plan_ids, b.note,
	c.created_at, c.used_at, c.subscription_id, s.plan_name, c.revoked_at`

const cardFrom = ` FROM reset_cards c JOIN reset_card_batches b ON b.id = c.batch_id JOIN users u ON u.id = c.user_id
	LEFT JOIN subscriptions s ON s.id = c.subscription_id`

// loadCards reads cards with sql (cardCols + cardFrom + conditions) and
// resolves plan names; now derives the expired status.
func loadCards(ctx context.Context, q db.Querier, now time.Time, sql string, args ...any) ([]*Card, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Card{}
	var allPlans []uuid.UUID
	for rows.Next() {
		var c Card
		var plans []byte
		var subID *uuid.UUID
		var planName *string
		if err := rows.Scan(&c.ID, &c.BatchID, &c.User.ID, &c.User.DisplayName, &c.Kind, &c.Status, &c.ExpiresAt, &plans,
			&c.Note, &c.CreatedAt, &c.UsedAt, &subID, &planName, &c.RevokedAt); err != nil {
			return nil, err
		}
		if c.planIDs, err = decodePlanIDs(plans); err != nil {
			return nil, err
		}
		allPlans = append(allPlans, c.planIDs...)
		c.ExpiresAt, c.UsedAt, c.RevokedAt, c.CreatedAt = utcPtr(c.ExpiresAt), utcPtr(c.UsedAt), utcPtr(c.RevokedAt), c.CreatedAt.UTC()
		c.Status = cardStatus(c.Status, c.ExpiresAt, now)
		if subID != nil {
			c.Subscription = &CardSubRef{ID: *subID}
			if planName != nil {
				c.Subscription.PlanName = *planName
			}
		}
		out = append(out, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	names, err := planNames(ctx, q, dedupeUUIDs(allPlans))
	if err != nil {
		return nil, err
	}
	for _, c := range out {
		c.Plans = planRefs(c.planIDs, names)
	}
	return out, nil
}

// CardFilter narrows the admin card list.
type CardFilter struct {
	UserID  *uuid.UUID
	BatchID *uuid.UUID
}

// ListCards returns cards newest first (admin; user management).
func (s *Service) ListCards(ctx context.Context, f CardFilter, offset, limit int) ([]*Card, int, error) {
	conds, args := []string{"true"}, []any{}
	if f.UserID != nil {
		args = append(args, *f.UserID)
		conds = append(conds, "c.user_id = $"+strconv.Itoa(len(args)))
	}
	if f.BatchID != nil {
		args = append(args, *f.BatchID)
		conds = append(conds, "c.batch_id = $"+strconv.Itoa(len(args)))
	}
	where := " WHERE " + strings.Join(conds, " AND ")
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM reset_cards c`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	n := len(args)
	items, err := loadCards(ctx, s.pool, dbTime(s.now()), `SELECT `+cardCols+cardFrom+where+
		` ORDER BY c.created_at DESC, c.id DESC LIMIT $`+strconv.Itoa(n-1)+` OFFSET $`+strconv.Itoa(n), args...)
	return items, total, err
}

// MyCards is the user's card list: usable cards first (soonest to expire
// first), then used, expired and revoked ones, newest first; at most 500.
type MyCards struct {
	Items []*Card `json:"items"`
	// Available counts the usable cards by kind (every kind present).
	Available map[string]int `json:"available"`
}

// ListMyCards returns the user's cards (§2.2).
func (s *Service) ListMyCards(ctx context.Context, userID uuid.UUID) (*MyCards, error) {
	now := dbTime(s.now())
	usable := `c.status = 'available' AND (c.expires_at IS NULL OR c.expires_at > $2)`
	items, err := loadCards(ctx, s.pool, now, `SELECT `+cardCols+cardFrom+` WHERE c.user_id = $1
		ORDER BY CASE WHEN `+usable+` THEN 0 ELSE 1 END,
			CASE WHEN `+usable+` AND c.expires_at IS NULL THEN 1 ELSE 0 END,
			CASE WHEN `+usable+` THEN c.expires_at END, c.created_at DESC, c.id DESC
		LIMIT $3`, userID, now, maxMyCards)
	if err != nil {
		return nil, err
	}
	out := &MyCards{Items: items, Available: map[string]int{CardKind5h: 0, CardKindWeekly: 0, CardKindBoth: 0}}
	rows, err := s.pool.Query(ctx, `SELECT c.kind, count(*) FROM reset_cards c WHERE c.user_id = $1 AND `+usable+` GROUP BY c.kind`,
		userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out.Available[kind] = n
	}
	return out, rows.Err()
}

// CardTargetSub is a subscription a card can be used on, with the current
// state of the rules it would reset (§2.3).
type CardTargetSub struct {
	ID       uuid.UUID   `json:"id"`
	Plan     IDName      `json:"plan"`
	EndsAt   time.Time   `json:"endsAt"`
	Rules    []RuleUsage `json:"rules"`
	HasUsage bool        `json:"hasUsage"` // some affected window has usage
}

// CardPreview is the response of GET /billing/reset-cards/{id}/preview.
type CardPreview struct {
	Card          *Card           `json:"card"`
	Now           time.Time       `json:"now"`
	Subscriptions []CardTargetSub `json:"subscriptions"`
}

// getMyCard loads one card of userID (404 for unknown or someone else's).
func getMyCard(ctx context.Context, q db.Querier, userID, id uuid.UUID, now time.Time) (*Card, error) {
	cards, err := loadCards(ctx, q, now, `SELECT `+cardCols+cardFrom+` WHERE c.id = $1 AND c.user_id = $2`, id, userID)
	if err != nil {
		return nil, err
	}
	if len(cards) == 0 {
		return nil, apperr.NotFound("重置卡")
	}
	return cards[0], nil
}

// cardAllows reports whether a card restricted to planIDs (empty = any) may
// be used on a subscription of planID.
func cardAllows(planIDs []uuid.UUID, planID uuid.UUID) bool {
	return len(planIDs) == 0 || slices.Contains(planIDs, planID)
}

// PreviewCard lists the user's live subscriptions the card applies to, with
// the current state of the rules it would reset. Only usable cards have
// targets; for others Subscriptions is empty.
func (s *Service) PreviewCard(ctx context.Context, userID, id uuid.UUID) (*CardPreview, error) {
	now := dbTime(s.now())
	c, err := getMyCard(ctx, s.pool, userID, id, now)
	if err != nil {
		return nil, err
	}
	out := &CardPreview{Card: c, Now: now, Subscriptions: []CardTargetSub{}}
	if c.Status != CardAvailable {
		return out, nil
	}
	subs, err := s.LiveSubscriptions(ctx, userID, now)
	if err != nil {
		return nil, err
	}
	var loaded []*loadedSub
	var keys []usageQuery
	for _, sub := range subs {
		if !cardAllows(c.planIDs, sub.PlanID) {
			continue
		}
		ls, err := compileSub(sub)
		if err != nil {
			s.log.ErrorContext(ctx, "skip subscription with invalid rules", "subscription", sub.ID, "err", err)
			continue
		}
		if ls.rules = cardRules(c.Kind, ls); len(ls.rules) == 0 {
			continue
		}
		loaded = append(loaded, ls)
		for _, r := range ls.rules {
			keys = append(keys, usageQuery{sub.ID, r.ID, r.usageSince(sub.StartsAt, now)})
		}
	}
	usage, err := loadUsage(ctx, s.pool, keys)
	if err != nil {
		return nil, err
	}
	for _, ls := range loaded {
		t := CardTargetSub{ID: ls.ID, Plan: IDName{ID: ls.PlanID, Name: ls.PlanName}, EndsAt: ls.EndsAt}
		for _, r := range ls.rules {
			st := r.state(ls.StartsAt, now, usage[usageKey{ls.ID, r.ID}])
			t.Rules = append(t.Rules, RuleUsage{Rule: r.Rule, Used: st.used.String(), WindowStart: st.windowStart,
				ResetsAt: st.resetsAt, Remaining: maxDec(r.limit.Sub(st.used), Dec{}).String(), Exceeded: st.exceeded})
			t.HasUsage = t.HasUsage || st.used.Sign() > 0
		}
		out.Subscriptions = append(out.Subscriptions, t)
	}
	return out, nil
}

// UseCardResult is the response of POST /billing/reset-cards/{id}/use.
type UseCardResult struct {
	Card         *Card             `json:"card"`
	Subscription *SubscriptionView `json:"subscription"`
	// Rules are the ids of the rules that were reset.
	Rules []string `json:"rules"`
}

// UseCard spends one of the actor's cards on one of their live subscriptions
// (§2.3) in one transaction: the card is claimed with a conditional update
// (so concurrent uses of one card succeed once), the subscription is checked
// (owner, active, plan restriction), the matching rules get the anchored
// reset and the use is audited. When no rule matches, nothing changes and the
// card stays available.
func (s *Service) UseCard(ctx context.Context, actor Actor, id, subID uuid.UUID, meta RequestMeta) (*UseCardResult, error) {
	now := dbTime(s.now())
	var res UseCardResult
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		res = UseCardResult{}
		var batchID uuid.UUID
		var kind string
		err := tx.QueryRow(ctx, `UPDATE reset_cards SET status = 'used', used_at = $3
			WHERE id = $1 AND user_id = $2 AND status = 'available' AND (expires_at IS NULL OR expires_at > $3)
			RETURNING batch_id, kind`, id, actor.ID, now).Scan(&batchID, &kind)
		if db.IsNoRows(err) {
			return s.unusableCard(ctx, tx, actor.ID, id, now)
		}
		if err != nil {
			return err
		}
		var plans []byte
		if err := tx.QueryRow(ctx, `SELECT plan_ids FROM reset_card_batches WHERE id = $1`, batchID).Scan(&plans); err != nil {
			return err
		}
		planIDs, err := decodePlanIDs(plans)
		if err != nil {
			return err
		}
		// Locking the subscription serializes the reset with Record (no session
		// can open between the delete and the anchor).
		sub, err := getSub(ctx, tx, subID, true)
		if err != nil {
			return err
		}
		if sub.UserID != actor.ID {
			return apperr.NotFound("订阅")
		}
		if sub.StatusAt(now) != StatusActive || sub.StartsAt.After(now) {
			return NotActiveError()
		}
		if !cardAllows(planIDs, sub.PlanID) {
			return CardPlanError()
		}
		ls, err := compileSub(sub)
		if err != nil {
			return err
		}
		rules := cardRules(kind, ls)
		if len(rules) == 0 {
			return CardNotApplicableError(kind)
		}
		if err := resetRules(ctx, tx, sub, rules, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reset_cards SET subscription_id = $2 WHERE id = $1`, id, sub.ID); err != nil {
			return err
		}
		for _, r := range rules {
			res.Rules = append(res.Rules, r.ID)
		}
		if res.Subscription, err = DescribeTx(ctx, tx, sub, now); err != nil {
			return err
		}
		if res.Card, err = getMyCard(ctx, tx, actor.ID, id, now); err != nil {
			return err
		}
		labels := make([]string, len(rules))
		for i, r := range rules {
			labels[i] = r.DisplayName()
		}
		return s.audit(ctx, tx, actor, meta, ActionCardUse, "reset_card", id.String(), map[string]any{
			"batchId": batchID, "kind": kind, "subscriptionId": sub.ID, "planName": sub.PlanName,
			"rules": res.Rules, "ruleLabels": labels, "resetAt": now,
		})
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// unusableCard explains why a card could not be claimed.
func (s *Service) unusableCard(ctx context.Context, q db.Querier, userID, id uuid.UUID, now time.Time) error {
	var status string
	var expiresAt *time.Time
	err := q.QueryRow(ctx, `SELECT status, expires_at FROM reset_cards WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&status, &expiresAt)
	if db.IsNoRows(err) {
		return apperr.NotFound("重置卡")
	}
	if err != nil {
		return err
	}
	switch cardStatus(status, expiresAt, now) {
	case CardUsed:
		return cardConflict("card_used", "该重置卡已使用")
	case CardRevoked:
		return cardConflict("card_revoked", "该重置卡已作废")
	case CardExpired:
		return cardConflict("card_expired", "该重置卡已过期")
	}
	return fmt.Errorf("subscription: card %s not claimable in state %q", id, status)
}
