package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"omnigate/internal/apperr"
	"omnigate/internal/authz"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

// AudienceFilter is shared by benefit eligibility, card recipients and bulk
// subscription operations. Lists are OR internally; different fields are AND.
// Subscription predicates always apply to the SAME live subscription, and quota
// windows are read from its snapshot, not the current plan's mutable rules.
type AudienceFilter struct {
	UserIDs                   []uuid.UUID `json:"userIds"`
	GroupIDs                  []uuid.UUID `json:"groupIds"`
	PlanIDs                   []uuid.UUID `json:"planIds"`
	ExcludePlanIDs            []uuid.UUID `json:"excludePlanIds"`
	Sources                   []string    `json:"sources"`
	RequireActiveSubscription bool        `json:"requireActiveSubscription"`
	ExcludeHiddenPlans        bool        `json:"excludeHiddenPlans"`
	QuotaKind                 string      `json:"quotaKind"`
	RegisteredAfter           *time.Time  `json:"registeredAfter"`
	RegisteredBefore          *time.Time  `json:"registeredBefore"`
	MinSpend                  string      `json:"minSpend"`
	RequirePurchase           bool        `json:"requirePurchase"`
}

func decodeAudienceFilter(raw []byte, f *AudienceFilter) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(f)
}

func (f AudienceFilter) NeedsSubscription() bool {
	return f.RequireActiveSubscription || f.ExcludeHiddenPlans || f.QuotaKind != "" || len(f.PlanIDs)+len(f.ExcludePlanIDs)+len(f.Sources) > 0
}

func (f AudienceFilter) Validate() error {
	for _, ids := range [][]uuid.UUID{f.UserIDs, f.GroupIDs, f.PlanIDs, f.ExcludePlanIDs} {
		if len(ids) > 1000 {
			return apperr.Validation("筛选名单最多 1000 项", nil)
		}
		for _, id := range ids {
			if id == uuid.Nil {
				return apperr.Validation("筛选不能包含空 ID", nil)
			}
		}
	}
	for _, source := range f.Sources {
		if source != SourceAdmin && source != SourceRedeem && source != SourcePurchase {
			return apperr.Validation("订阅来源筛选无效", nil)
		}
	}
	if f.QuotaKind != "" && !ValidCardKind(f.QuotaKind) {
		return apperr.Validation("额度窗口筛选必须为 5h、weekly 或 both", nil)
	}
	if f.RegisteredAfter != nil && f.RegisteredBefore != nil && !f.RegisteredAfter.Before(*f.RegisteredBefore) {
		return apperr.Validation("注册时间范围无效", nil)
	}
	if f.MinSpend != "" {
		v, err := money.Parse(f.MinSpend)
		if err != nil || v < 0 {
			return apperr.Validation("累计模型消费筛选无效", nil)
		}
	}
	return nil
}

type audienceSQL struct {
	parts []string
	args  []any
}

func (b *audienceSQL) add(expr string, value any) {
	b.args = append(b.args, value)
	b.parts = append(b.parts, strings.ReplaceAll(expr, "$?", fmt.Sprintf("$%d", len(b.args))))
}

func audienceWhere(ctx context.Context, q db.Querier, f AudienceFilter) (*audienceSQL, error) {
	if err := ValidateAudienceReferences(ctx, q, f); err != nil {
		return nil, err
	}
	b := &audienceSQL{parts: []string{"u.status = 'active'"}}
	b.add("u.role = ANY($?)", rolesWith(authz.BillingOwn))
	if len(f.UserIDs) > 0 {
		b.add("u.id = ANY($?)", f.UserIDs)
	}
	if len(f.GroupIDs) > 0 {
		b.add("u.group_id = ANY($?)", f.GroupIDs)
	}
	if f.RegisteredAfter != nil {
		b.add("u.created_at >= $?", *f.RegisteredAfter)
	}
	if f.RegisteredBefore != nil {
		b.add("u.created_at < $?", *f.RegisteredBefore)
	}
	if f.RequirePurchase {
		b.parts = append(b.parts, "EXISTS (SELECT 1 FROM plan_purchases pp WHERE pp.user_id=u.id AND pp.price_nano>0)")
	}
	if f.MinSpend != "" && f.MinSpend != "0" {
		v, _ := money.Parse(f.MinSpend)
		b.add("(SELECT COALESCE(SUM(ld.spent_nano),0) FROM lottery_days ld WHERE ld.user_id=u.id) >= $?", int64(v))
	}
	return b, nil
}

// AudienceSubscriptions returns the matching active subscription instances.
// Optional IDs narrow explicit selections without changing filter semantics.
func AudienceSubscriptions(ctx context.Context, q db.Querier, f AudienceFilter, now time.Time, lock bool, ids []uuid.UUID) ([]*Subscription, error) {
	b, err := audienceWhere(ctx, q, f)
	if err != nil {
		return nil, err
	}
	b.parts = append(b.parts, "s.status='active'")
	b.add("s.starts_at <= $? AND s.ends_at > $?", now)
	if ids != nil {
		b.add("s.id = ANY($?)", ids)
	}
	if len(f.PlanIDs) > 0 {
		b.add("s.plan_id = ANY($?)", f.PlanIDs)
	}
	if len(f.ExcludePlanIDs) > 0 {
		b.add("NOT (s.plan_id = ANY($?))", f.ExcludePlanIDs)
	}
	if len(f.Sources) > 0 {
		b.add("s.source = ANY($?)", f.Sources)
	}
	if f.ExcludeHiddenPlans {
		b.parts = append(b.parts, "ap.hidden=false")
	}
	sql := "SELECT " + subCols + subFrom + " JOIN plans ap ON ap.id=s.plan_id WHERE " + strings.Join(b.parts, " AND ") + " ORDER BY s.created_at,s.id"
	if lock {
		sql += " FOR UPDATE OF s"
	}
	rows, err := q.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, err
	}
	subs, err := collectSubs(rows)
	if err != nil {
		return nil, err
	}
	if f.QuotaKind == "" {
		return subs, nil
	}
	matched := []*Subscription{}
	for _, sub := range subs {
		ls, err := compileSub(sub)
		if err != nil {
			return nil, err
		}
		if len(cardRules(f.QuotaKind, ls)) > 0 {
			matched = append(matched, sub)
		}
	}
	return matched, nil
}

type AudienceUser struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}
type AudiencePreview struct {
	Users         int            `json:"users"`
	Subscriptions int            `json:"subscriptions"`
	Sample        []AudienceUser `json:"sample"`
	Truncated     bool           `json:"truncated"`
	AsOf          time.Time      `json:"asOf"`
}

// AudienceUsers deduplicates users across subscriptions. Additional restrict
// IDs intersect the filter, allowing a claim to recheck just its own user.
func AudienceUsers(ctx context.Context, q db.Querier, f AudienceFilter, now time.Time, restrict []uuid.UUID) ([]AudienceUser, int, error) {
	if err := f.Validate(); err != nil {
		return nil, 0, err
	}
	if restrict != nil {
		if len(restrict) == 0 {
			return []AudienceUser{}, 0, nil
		}
		if len(f.UserIDs) > 0 {
			matched := []uuid.UUID{}
			for _, id := range restrict {
				if slices.Contains(f.UserIDs, id) {
					matched = append(matched, id)
				}
			}
			if len(matched) == 0 {
				return []AudienceUser{}, 0, nil
			}
			f.UserIDs = matched
		} else {
			f.UserIDs = restrict
		}
	}
	if f.NeedsSubscription() {
		subs, err := AudienceSubscriptions(ctx, q, f, now, false, nil)
		if err != nil {
			return nil, 0, err
		}
		users := []AudienceUser{}
		seen := map[uuid.UUID]bool{}
		for _, sub := range subs {
			if !seen[sub.UserID] {
				seen[sub.UserID] = true
				users = append(users, AudienceUser{ID: sub.UserID, Name: sub.UserName})
			}
		}
		return users, len(subs), nil
	}
	b, err := audienceWhere(ctx, q, f)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, "SELECT u.id,u.display_name FROM users u WHERE "+strings.Join(b.parts, " AND ")+" ORDER BY u.id", b.args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	users := []AudienceUser{}
	for rows.Next() {
		var u AudienceUser
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, 0, rows.Err()
}

func PreviewAudience(ctx context.Context, q db.Querier, f AudienceFilter, now time.Time) (*AudiencePreview, error) {
	users, subs, err := AudienceUsers(ctx, q, f, now, nil)
	if err != nil {
		return nil, err
	}
	sample := users
	if len(sample) > 50 {
		sample = sample[:50]
	}
	return &AudiencePreview{Users: len(users), Subscriptions: subs, Sample: sample, Truncated: len(sample) < len(users), AsOf: now}, nil
}

// ValidateAudienceReferences is used when saving conditions and when resolving
// recipients so invalid group/plan selections cannot become saved activities.
func ValidateAudienceReferences(ctx context.Context, q db.Querier, f AudienceFilter) error {
	if err := f.Validate(); err != nil {
		return err
	}
	for _, check := range []struct {
		table, label string
		ids          []uuid.UUID
	}{{"user_groups", "用户组", f.GroupIDs}, {"plans", "套餐", f.PlanIDs}, {"plans", "排除套餐", f.ExcludePlanIDs}} {
		if err := checkExist(ctx, q, check.table, check.label, dedupeUUIDs(check.ids)); err != nil {
			return err
		}
	}
	return nil
}
