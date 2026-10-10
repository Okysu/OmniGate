package subscription

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/platform/db"
)

// Bulk subscription operations (docs/contracts/phase7-api.md §3): resetting
// the current quota windows and extending subscriptions, for explicit
// subscriptions or every active subscription of a plan, with a dry run.

// Audit actions of the bulk operations.
const (
	ActionQuotaReset = "subscription.quota_reset"
	ActionExtend     = "subscription.extend"
)

const (
	maxBulkIDs     = 1000 // explicit target ids per request
	maxBulkNote    = 200
	maxBulkRules   = 50
	maxBulkListed  = 500 // ids returned (and audited)
	bulkKindReset  = "quota_reset"
	bulkKindExtend = "extended"
)

// Target selects subscriptions: explicit ids, or every active subscription
// of a plan (PlanID nil = every plan). Only active, unexpired subscriptions
// are ever affected.
type Target struct {
	IDs    []uuid.UUID
	ByPlan bool
	PlanID *uuid.UUID
}

// auditJSON renders the target as in the request.
func (t Target) auditJSON() map[string]any {
	if !t.ByPlan {
		return map[string]any{"ids": t.IDs}
	}
	return map[string]any{"planId": t.PlanID, "status": StatusActive}
}

// ResetInput is the body of POST /admin/billing/subscriptions/reset-quota.
type ResetInput struct {
	Target          Target
	Rules           []string // nil = every rule
	IncludeLifetime bool
	Note            string
}

// ExtendInput is the body of POST /admin/billing/subscriptions/extend.
type ExtendInput struct {
	Target   Target
	Duration string
	Note     string
}

// BulkResult is the response of the bulk operations (also for dry runs).
type BulkResult struct {
	Affected      int         `json:"affected"`
	Subscriptions []uuid.UUID `json:"subscriptions"`
}

// BulkItem is one affected subscription.
type BulkItem struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	PlanName       string
	EndsAt         time.Time // after the operation
	Rules          []string  // reset: the rules that were reset (labels)
}

// BulkNotice describes a committed bulk operation (notifications).
type BulkNotice struct {
	Kind        string // "quota_reset" | "extended"
	OperationID uuid.UUID
	Note        string
	Duration    string // extended
	Items       []BulkItem
}

// IsReset reports whether n is a quota reset (otherwise an extension).
func (n BulkNotice) IsReset() bool { return n.Kind == bulkKindReset }

func validateTarget(t Target, details map[string]any) {
	if !t.ByPlan && (len(t.IDs) == 0 || len(t.IDs) > maxBulkIDs) {
		details["target"] = fmt.Sprintf("ids 必须有 1 到 %d 个订阅 ID", maxBulkIDs)
	}
}

func validateNote(note string, details map[string]any) string {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > maxBulkNote {
		details["note"] = fmt.Sprintf("不能超过 %d 个字符", maxBulkNote)
	}
	return note
}

// targetSubs loads the active subscriptions of t at now (locked for update
// unless dry), oldest first.
func (s *Service) targetSubs(ctx context.Context, q db.Querier, t Target, now time.Time, lock bool) ([]*Subscription, error) {
	where, args := ` WHERE s.status = 'active' AND s.ends_at > $1`, []any{now}
	switch {
	case !t.ByPlan:
		args = append(args, t.IDs)
		where += ` AND s.id = ANY($2)`
	case t.PlanID != nil:
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plans WHERE id = $1)`, *t.PlanID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, apperr.NotFound("套餐")
		}
		args = append(args, *t.PlanID)
		where += ` AND s.plan_id = $2`
	}
	sql := `SELECT ` + subCols + subFrom + where + ` ORDER BY s.created_at, s.id`
	if lock {
		sql += ` FOR UPDATE OF s`
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return collectSubs(rows)
}

func result(ids []uuid.UUID) *BulkResult {
	listed := ids
	if len(listed) > maxBulkListed {
		listed = listed[:maxBulkListed]
	}
	if listed == nil {
		listed = []uuid.UUID{}
	}
	return &BulkResult{Affected: len(ids), Subscriptions: listed}
}

// resettable returns the rules of sub that a reset with in clears.
func resettable(sub *Subscription, in ResetInput) ([]*compiledRule, error) {
	ls, err := compileSub(sub)
	if err != nil {
		return nil, err
	}
	var out []*compiledRule
	for _, r := range ls.rules {
		if in.Rules != nil && !slices.Contains(in.Rules, r.ID) {
			continue
		}
		if r.Window.Kind == WindowLifetime && !in.IncludeLifetime {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// resetRules clears the current window of rules of sub at now inside the
// caller's transaction (phase7-api.md §3.1, phase11-api.md §1). Rows at or
// after the start of the current window are exactly the ones the window's
// state is computed from, so they are deleted: calendar / period / lifetime
// windows lose their counter and rolling windows every live bucket (rolling
// windows have no single refresh time to move). A session window is
// re-anchored at now: an empty session row starting at now (truncated to the
// second like recordWindow) becomes the live session, so the window refreshes
// at now + duration and later usage is added to it. Sweep keeps empty rows
// like any other until they are older than every window.
func resetRules(ctx context.Context, q db.Querier, sub *Subscription, rules []*compiledRule, now time.Time) error {
	for _, r := range rules {
		if _, err := q.Exec(ctx, `DELETE FROM quota_usage WHERE subscription_id = $1 AND rule_id = $2 AND window_start >= $3`,
			sub.ID, r.ID, r.usageSince(sub.StartsAt, now)); err != nil {
			return err
		}
		if r.Window.Kind != WindowSession {
			continue
		}
		if _, err := q.Exec(ctx, `INSERT INTO quota_usage (subscription_id, rule_id, window_start, used, updated_at)
			VALUES ($1, $2, $3, '0', $4)
			ON CONFLICT (subscription_id, rule_id, window_start) DO UPDATE SET used = EXCLUDED.used, updated_at = EXCLUDED.updated_at`,
			sub.ID, r.ID, sessionAnchor(now), now); err != nil {
			return err
		}
	}
	return nil
}

// sessionAnchor is the start of the session a reset at now opens.
func sessionAnchor(now time.Time) time.Time { return now.UTC().Truncate(time.Second) }

// ResetQuota clears the current window of the selected rules of every target
// subscription (§3.1, see resetRules): calendar / period windows lose their
// current counter, rolling windows every bucket inside the window, a session
// window restarts at the time of the reset (usage 0, refreshing one duration
// later, like a global quota reset) and lifetime counters are only cleared
// with IncludeLifetime. Subscriptions without a resettable rule are not
// affected. Gateway quota decisions read the counters on every request, so
// blocked users are admitted again immediately.
func (s *Service) ResetQuota(ctx context.Context, actor Actor, in ResetInput, dryRun bool, meta RequestMeta) (*BulkResult, error) {
	details := map[string]any{}
	validateTarget(in.Target, details)
	in.Note = validateNote(in.Note, details)
	if in.Rules != nil {
		if len(in.Rules) == 0 || len(in.Rules) > maxBulkRules {
			details["rules"] = fmt.Sprintf("为 null（全部规则）或 1 到 %d 个规则 ID", maxBulkRules)
		}
		for _, id := range in.Rules {
			if !ruleIDPattern.MatchString(id) {
				details["rules"] = "规则 ID 只能包含小写字母、数字、- 和 _，长度 1–32"
				break
			}
		}
	}
	if len(details) > 0 {
		return nil, apperr.Validation("参数校验失败", details)
	}
	now := dbTime(s.now())
	var ids []uuid.UUID
	var items []BulkItem
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		ids, items = nil, nil
		subs, err := s.targetSubs(ctx, tx, in.Target, now, !dryRun)
		if err != nil {
			return err
		}
		for _, sub := range subs {
			rules, err := resettable(sub, in)
			if err != nil {
				s.log.ErrorContext(ctx, "skip subscription with invalid rules", "subscription", sub.ID, "err", err)
				continue
			}
			if len(rules) == 0 {
				continue
			}
			ids = append(ids, sub.ID)
			if dryRun {
				continue
			}
			item := BulkItem{SubscriptionID: sub.ID, UserID: sub.UserID, PlanName: sub.PlanName, EndsAt: sub.EndsAt}
			if err := resetRules(ctx, tx, sub, rules, now); err != nil {
				return err
			}
			for _, r := range rules {
				item.Rules = append(item.Rules, r.DisplayName())
			}
			items = append(items, item)
		}
		if dryRun {
			return nil
		}
		return s.rec.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, ActorName: &actor.Name, Action: ActionQuotaReset,
			ResourceType: "subscription", IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
			Metadata: map[string]any{"target": in.Target.auditJSON(), "rules": in.Rules, "includeLifetime": in.IncludeLifetime,
				"note": in.Note, "affected": len(ids), "subscriptions": result(ids).Subscriptions}})
	})
	if err != nil {
		return nil, err
	}
	if !dryRun && len(items) > 0 && s.OnBulk != nil {
		s.OnBulk(ctx, BulkNotice{Kind: bulkKindReset, OperationID: uuid.Must(uuid.NewV7()), Note: in.Note, Items: items})
	}
	return result(ids), nil
}

// Extend moves endsAt of every target subscription by the duration (§3.2).
func (s *Service) Extend(ctx context.Context, actor Actor, in ExtendInput, dryRun bool, meta RequestMeta) (*BulkResult, error) {
	details := map[string]any{}
	validateTarget(in.Target, details)
	in.Note = validateNote(in.Note, details)
	in.Duration = strings.TrimSpace(in.Duration)
	d, err := ParsePeriod(in.Duration)
	if in.Duration == "" {
		details["duration"] = "必填"
	} else if err != nil {
		details["duration"] = err.Error()
	}
	if len(details) > 0 {
		return nil, apperr.Validation("参数校验失败", details)
	}
	now := dbTime(s.now())
	var ids []uuid.UUID
	var items []BulkItem
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		ids, items = nil, nil
		subs, err := s.targetSubs(ctx, tx, in.Target, now, !dryRun)
		if err != nil {
			return err
		}
		for _, sub := range subs {
			ids = append(ids, sub.ID)
			if dryRun {
				continue
			}
			ends := sub.EndsAt.Add(d)
			if _, err := tx.Exec(ctx, `UPDATE subscriptions SET ends_at = $2, version = version + 1, updated_at = $3 WHERE id = $1`,
				sub.ID, ends, now); err != nil {
				return err
			}
			items = append(items, BulkItem{SubscriptionID: sub.ID, UserID: sub.UserID, PlanName: sub.PlanName, EndsAt: ends})
		}
		if dryRun {
			return nil
		}
		return s.rec.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, ActorName: &actor.Name, Action: ActionExtend,
			ResourceType: "subscription", IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
			Metadata: map[string]any{"target": in.Target.auditJSON(), "duration": in.Duration, "note": in.Note,
				"affected": len(ids), "subscriptions": result(ids).Subscriptions}})
	})
	if err != nil {
		return nil, err
	}
	if !dryRun && len(items) > 0 && s.OnBulk != nil {
		s.OnBulk(ctx, BulkNotice{Kind: bulkKindExtend, OperationID: uuid.Must(uuid.NewV7()), Note: in.Note, Duration: in.Duration, Items: items})
	}
	return result(ids), nil
}
