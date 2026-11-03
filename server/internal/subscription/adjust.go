package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/audit"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
)

const ActionQuotaAdjust = "subscription.quota_adjust"

// AdjustQuotaInput changes one rule in existing subscription snapshots. Nil
// values leave that part unchanged. Remaining is the current window's balance.
type AdjustQuotaInput struct {
	Target    Target
	RuleID    string
	Limit     *string
	Remaining *string
	Note      string
}

func (h *Handler) adjustQuota(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target    json.RawMessage `json:"target"`
		RuleID    string          `json:"ruleId"`
		Limit     *string         `json:"limit"`
		Remaining *string         `json:"remaining"`
		Note      string          `json:"note"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	in := AdjustQuotaInput{Target: parseTarget(body.Target, details), RuleID: body.RuleID, Limit: body.Limit, Remaining: body.Remaining, Note: body.Note}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	res, err := h.svc.AdjustQuota(r.Context(), actor(r), in, dryRun(r), meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// saveQuotaRules only updates the rules snapshot; expiry and usage anchors are
// deliberately independent of plan and subscription capacity edits.
func saveQuotaRules(ctx context.Context, q db.Querier, sub *Subscription, now time.Time) error {
	raw, err := json.Marshal(sub.Rules)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `UPDATE subscriptions SET rules = $2, version = version + 1, updated_at = $3 WHERE id = $1`, sub.ID, raw, now)
	return err
}

func compatibleLimit(a, b Rule) bool {
	return a.Meter == b.Meter && reflect.DeepEqual(a.Window, b.Window) &&
		slices.Equal(a.Models, b.Models) && reflect.DeepEqual(a.ModelWeights, b.ModelWeights) &&
		reflect.DeepEqual(a.PluginVersionID, b.PluginVersionID)
}

func (s *Service) syncPlanLimits(ctx context.Context, q db.Querier, plan *Plan, now time.Time) (int, error) {
	subs, err := s.targetSubs(ctx, q, Target{ByPlan: true, PlanIDs: []uuid.UUID{plan.ID}}, now, true)
	if err != nil {
		return 0, err
	}
	byID := map[string]Rule{}
	for _, r := range plan.Rules {
		byID[r.ID] = r
	}
	count := 0
	for _, sub := range subs {
		changed := false
		for i, old := range sub.Rules {
			next, ok := byID[old.ID]
			if !ok {
				continue
			}
			if !compatibleLimit(old, next) {
				return 0, invalid(map[string]any{"syncSubscriptionLimits": fmt.Sprintf("订阅 %s 的规则 %s 与新规则计量或窗口不同；请仅同步上限，或在订阅管理中单独调整", sub.ID, old.ID)})
			}
			if old.Limit != next.Limit {
				sub.Rules[i].Limit = next.Limit
				changed = true
			}
		}
		if changed {
			if err := saveQuotaRules(ctx, q, sub, now); err != nil {
				return 0, err
			}
			count++
		}
	}
	return count, nil
}

// setWindowUsed keeps all existing bucket timestamps. For rolling windows,
// reductions consume the newest usage first; increases go to the oldest live
// bucket so an adjustment cannot postpone the existing refresh schedule.
func setWindowUsed(ctx context.Context, tx db.Tx, sub *Subscription, r *compiledRule, rows []usageRow, want Dec, now time.Time) error {
	st := r.state(sub.StartsAt, now, rows)
	if st.used.Cmp(want) == 0 {
		return nil
	}
	if r.Window.Kind != WindowRolling {
		if st.windowStart == nil {
			return invalid(map[string]any{"remaining": "会话窗口尚未开始；可修改上限，首次使用后再调整余量"})
		}
		return addUsage(ctx, tx, sub.ID, r.ID, *st.windowStart, want.Sub(st.used), now)
	}
	live := slices.DeleteFunc(slices.Clone(rows), func(row usageRow) bool { return !row.start.After(now.Add(-r.dur)) })
	if len(live) == 0 {
		return invalid(map[string]any{"remaining": "滚动窗口尚无用量；可修改上限，首次使用后再调整余量"})
	}
	slices.SortFunc(live, func(a, b usageRow) int { return a.start.Compare(b.start) })
	delta := want.Sub(st.used)
	if delta.Sign() > 0 {
		return addUsage(ctx, tx, sub.ID, r.ID, live[0].start, delta, now)
	}
	remove := st.used.Sub(want)
	for i := len(live) - 1; i >= 0 && remove.Sign() > 0; i-- {
		take := live[i].used
		if take.Cmp(remove) > 0 {
			take = remove
		}
		if take.Sign() == 0 {
			continue
		}
		if err := addUsage(ctx, tx, sub.ID, r.ID, live[i].start, Dec{}.Sub(take), now); err != nil {
			return err
		}
		remove = remove.Sub(take)
	}
	return nil
}

func (s *Service) AdjustQuota(ctx context.Context, actor Actor, in AdjustQuotaInput, dry bool, meta RequestMeta) (*BulkResult, error) {
	details := map[string]any{}
	validateTarget(in.Target, details)
	in.Note = validateNote(in.Note, details)
	if !ruleIDPattern.MatchString(in.RuleID) {
		details["ruleId"] = "请选择有效的规则 ID"
	}
	if in.Limit == nil && in.Remaining == nil {
		details["limit"] = "请填写上限或当前余量"
	}
	for key, value := range map[string]*string{"limit": in.Limit, "remaining": in.Remaining} {
		if value == nil {
			continue
		}
		d, err := ParseDec(*value)
		if err != nil || d.Sign() < 0 || (key == "limit" && d.Sign() == 0) {
			details[key] = "请输入有效的非负十进制数，上限必须大于 0"
		}
	}
	if len(details) > 0 {
		return nil, invalid(details)
	}
	now := dbTime(s.now())
	var ids []uuid.UUID
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		ids = nil
		changes := []map[string]any{}
		subs, err := s.targetSubs(ctx, tx, in.Target, now, !dry)
		if err != nil {
			return err
		}
		for _, sub := range subs {
			idx := slices.IndexFunc(sub.Rules, func(r Rule) bool { return r.ID == in.RuleID })
			if idx < 0 {
				continue
			}
			before := sub.Rules[idx]
			if in.Limit != nil {
				sub.Rules[idx].Limit = *in.Limit
			}
			compiled, err := compileRules(sub.Rules)
			if err != nil {
				return invalid(map[string]any{"limit": "上限不符合该订阅的计量规则：" + err.Error()})
			}
			r := compiled[idx]
			usage, err := loadUsage(ctx, tx, []usageQuery{{sub.ID, r.ID, r.usageSince(sub.StartsAt, now)}})
			if err != nil {
				return err
			}
			rows := usage[usageKey{sub.ID, r.ID}]
			st := r.state(sub.StartsAt, now, rows)
			used := st.used
			if in.Remaining != nil {
				remaining, _ := ParseDec(*in.Remaining)
				if remaining.Cmp(r.limit) > 0 || (r.Meter != MeterCharge && !isCustomMeter(r.Meter) && !remaining.IsInt()) {
					return invalid(map[string]any{"remaining": "余量不能超过上限，次数和 Token 等计量必须为整数"})
				}
				used = r.limit.Sub(remaining)
				hasLiveBucket := slices.ContainsFunc(rows, func(row usageRow) bool { return row.start.After(now.Add(-r.dur)) })
				if used.Sign() > 0 && (st.windowStart == nil || (r.Window.Kind == WindowRolling && !hasLiveBucket)) {
					return invalid(map[string]any{"remaining": "窗口尚未开始；请只调整上限，以保留首次使用时开始计时的规则"})
				}
			}
			ids = append(ids, sub.ID)
			if dry {
				continue
			}
			if in.Remaining != nil {
				if err := setWindowUsed(ctx, tx, sub, r, rows, used, now); err != nil {
					return err
				}
			}
			if err := saveQuotaRules(ctx, tx, sub, now); err != nil {
				return err
			}
			if len(changes) < maxBulkListed {
				changes = append(changes, map[string]any{"subscriptionId": sub.ID, "beforeLimit": before.Limit, "afterLimit": r.Limit, "beforeUsed": st.used.String(), "afterUsed": used.String(), "windowStart": st.windowStart, "resetsAt": st.resetsAt})
			}
		}
		if dry {
			return nil
		}
		return s.rec.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, ActorName: &actor.Name, Action: ActionQuotaAdjust, ResourceType: "subscription", IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
			Metadata: map[string]any{"target": in.Target.auditJSON(), "ruleId": in.RuleID, "limit": in.Limit, "remaining": in.Remaining, "note": in.Note, "affected": len(ids), "changes": changes}})
	})
	if err != nil {
		return nil, err
	}
	return result(ids), nil
}
