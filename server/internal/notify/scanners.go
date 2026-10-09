package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/authz"
)

const (
	expiryScanEvery = 10 * time.Minute
	modelScanEvery  = 10 * time.Minute
	subExpiringIn   = 3 * 24 * time.Hour
	keyExpiringIn   = 7 * 24 * time.Hour
	expiredLookback = 2 * 24 * time.Hour
	retentionDays   = 90
)

// RunScanners runs the periodic scanners until ctx is done: expiring
// subscriptions and keys every 10 minutes, the model diff on every registry
// reload that changed the channel set (and every 10 minutes).
func (s *Service) RunScanners(ctx context.Context) {
	expiry := time.NewTicker(expiryScanEvery)
	defer expiry.Stop()
	models := time.NewTicker(modelScanEvery)
	defer models.Stop()
	s.scanExpiryLogged(ctx)
	s.scanModelsLogged(ctx, true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			s.scanExpiryLogged(ctx)
		case <-models.C:
			s.scanModelsLogged(ctx, true)
		case <-s.modelScan:
			s.scanModelsLogged(ctx, false)
		}
	}
}

// RegistryReloaded asks for a model diff (channel.Registry.OnReload).
func (s *Service) RegistryReloaded() {
	select {
	case s.modelScan <- struct{}{}:
	default:
	}
}

func (s *Service) scanExpiryLogged(ctx context.Context) {
	if err := s.ScanExpiry(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("notification expiry scan failed", "err", err)
	}
}

func (s *Service) scanModelsLogged(ctx context.Context, force bool) {
	if s.opts.Models == nil {
		return
	}
	if changed := s.modelsChanged(); !force && !changed {
		return
	}
	if err := s.ScanModels(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("notification model scan failed", "err", err)
	}
}

// ScanExpiry emits subscription.expiring (3 days ahead, once per end date),
// subscription.expired (ended or cancelled) and key.expiring (7 days ahead).
func (s *Service) ScanExpiry(ctx context.Context) error {
	now := s.now()
	type sub struct {
		id, user  uuid.UUID
		plan      string
		ends      time.Time
		status    string
		cancelled *time.Time
	}
	rows, err := s.pool.Query(ctx, `SELECT id, user_id, plan_name, ends_at, status, cancelled_at FROM subscriptions
		WHERE (status = 'active' AND ends_at > $1 AND ends_at <= $2)
		   OR (status = 'active' AND ends_at <= $1 AND ends_at > $3)
		   OR (status = 'cancelled' AND cancelled_at > $3)`, now, now.Add(subExpiringIn), now.Add(-expiredLookback))
	if err != nil {
		return err
	}
	var subs []sub
	for rows.Next() {
		var x sub
		if err := rows.Scan(&x.id, &x.user, &x.plan, &x.ends, &x.status, &x.cancelled); err != nil {
			rows.Close()
			return err
		}
		subs = append(subs, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range subs {
		data := map[string]any{"subscriptionId": x.id, "planName": x.plan, "endsAt": x.ends.UTC()}
		switch {
		case x.status == "active" && x.ends.After(now):
			left := x.ends.Sub(now)
			days := int(left.Hours()/24) + 1
			s.emitLogged(ctx, Event{Type: TypeSubscriptionExpiring, Key: fmt.Sprintf("subscription.expiring:%s:%d", x.id, x.ends.Unix()),
				Users: []uuid.UUID{x.user}, Title: "套餐「" + x.plan + "」即将到期",
				Body: fmt.Sprintf("你的套餐「%s」将于 %s 到期（约 %d 天后）。到期后请求将按钱包余额计费。", x.plan, x.ends.UTC().Format("2006-01-02 15:04 UTC"), days),
				Link: "/console/billing", Data: data})
		default:
			body := fmt.Sprintf("你的套餐「%s」已于 %s 到期。", x.plan, x.ends.UTC().Format("2006-01-02 15:04 UTC"))
			if x.status == "cancelled" {
				body = fmt.Sprintf("你的套餐「%s」已被取消。", x.plan)
				data["cancelled"] = true
			}
			s.emitLogged(ctx, Event{Type: TypeSubscriptionExpired, Key: "subscription.expired:" + x.id.String(), Users: []uuid.UUID{x.user},
				Title: "套餐「" + x.plan + "」已结束", Body: body + "之后的请求将按钱包余额计费。", Link: "/console/billing", Data: data})
		}
	}

	krows, err := s.pool.Query(ctx, `SELECT id, user_id, name, prefix, expires_at FROM gateway_keys
		WHERE status = 'enabled' AND expires_at > $1 AND expires_at <= $2`, now, now.Add(keyExpiringIn))
	if err != nil {
		return err
	}
	type key struct {
		id, user     uuid.UUID
		name, prefix string
		exp          time.Time
	}
	var keys []key
	for krows.Next() {
		var k key
		if err := krows.Scan(&k.id, &k.user, &k.name, &k.prefix, &k.exp); err != nil {
			krows.Close()
			return err
		}
		keys = append(keys, k)
	}
	krows.Close()
	if err := krows.Err(); err != nil {
		return err
	}
	for _, k := range keys {
		s.emitLogged(ctx, Event{Type: TypeKeyExpiring, Key: fmt.Sprintf("key.expiring:%s:%d", k.id, k.exp.Unix()), Users: []uuid.UUID{k.user},
			Title: "API Key「" + k.name + "」即将到期",
			Body:  fmt.Sprintf("API Key「%s」（%s…）将于 %s 到期，到期后使用它的请求会被拒绝。", k.name, k.prefix, k.exp.UTC().Format("2006-01-02 15:04 UTC")),
			Link:  "/console/keys", Data: map[string]any{"keyId": k.id, "name": k.name, "expiresAt": k.exp.UTC()}})
	}
	return nil
}

// modelsSignature fingerprints what decides model availability (channels,
// scopes, shares, models); unchanged signatures skip reload-triggered scans.
func (s *Service) modelsSignature() string {
	var parts []string
	for _, rt := range s.opts.Models.Active() {
		ms := rt.ModelNames()
		slices.Sort(ms)
		users := rt.SharedUsers()
		sh := make([]string, len(users))
		for i, u := range users {
			sh[i] = u.String()
		}
		slices.Sort(sh)
		parts = append(parts, rt.ID.String()+"|"+string(rt.Scope)+"|"+rt.Owner.ID.String()+"|"+strconv.FormatBool(rt.PlatformOwned())+"|"+
			strings.Join(sh, ",")+"|"+strings.Join(ms, ","))
	}
	slices.Sort(parts)
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(h[:])
}

func (s *Service) modelsChanged() bool {
	sig := s.modelsSignature()
	s.mu.Lock()
	defer s.mu.Unlock()
	if sig == s.lastModelSig {
		return false
	}
	s.lastModelSig = sig
	return true
}

const (
	stateModelsInit    = "models.initialized"
	modelGoneKeyPrefix = "model_gone:"
	plazaKeyPrefix     = "plaza_model:"
)

// ScanModels diffs model availability: model.removed for users who called a
// model in the last 30 days that they can no longer use (re-armed when it
// comes back), model.added for new platform plaza models. The first scan only
// records the current state.
func (s *Service) ScanModels(ctx context.Context) error {
	if s.opts.Models == nil {
		return nil
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	now := s.now()
	var initialized bool
	ok, err := s.getState(ctx, stateModelsInit, &initialized)
	if err != nil {
		return err
	}
	first := !ok

	// Platform plaza models (global channels of administrators, not hidden).
	platform := map[string]bool{}
	for _, rt := range s.opts.Models.Active() {
		if rt.PlatformOwned() && rt.Scope == authz.ScopeGlobal {
			for _, m := range rt.ModelNames() {
				platform[m] = true
			}
		}
	}
	hidden := map[string]bool{}
	hrows, err := s.pool.Query(ctx, `SELECT model FROM model_info WHERE hidden`)
	if err != nil {
		return err
	}
	for hrows.Next() {
		var m string
		if err := hrows.Scan(&m); err != nil {
			hrows.Close()
			return err
		}
		hidden[m] = true
	}
	hrows.Close()
	// Every visible platform model has a "plaza_model:<m>" mark; a new mark
	// means a new model (cleared when the model disappears, so a model that
	// comes back is announced again).
	marks := map[string]bool{}
	mrows, err := s.pool.Query(ctx, `SELECT key FROM notification_state WHERE key LIKE 'plaza\_model:%' ESCAPE '\' OR key LIKE 'model\_gone:%' ESCAPE '\'`)
	if err != nil {
		return err
	}
	for mrows.Next() {
		var k string
		if err := mrows.Scan(&k); err != nil {
			mrows.Close()
			return err
		}
		marks[k] = true
	}
	mrows.Close()
	var users []uuid.UUID
	current := map[string]bool{}
	for m := range platform {
		if hidden[m] {
			continue
		}
		key := plazaKeyPrefix + m
		current[key] = true
		if marks[key] {
			continue
		}
		fresh, err := s.setMark(ctx, key, map[string]any{"at": now})
		if err != nil {
			return err
		}
		if !fresh || first {
			continue
		}
		if users == nil {
			if users, err = s.activeUsers(ctx); err != nil {
				return err
			}
		}
		s.emitLogged(ctx, Event{Type: TypeModelAdded, Key: fmt.Sprintf("model.added:%s:%d", m, now.UnixNano()), Subject: m, Users: users,
			Title: "新模型上架：" + m, Body: "模型广场新增了模型 " + m + "，现在就可以调用。", Link: "/console/plaza",
			Data: map[string]any{"model": m}})
	}
	for k := range marks {
		if strings.HasPrefix(k, plazaKeyPrefix) && !current[k] {
			if err := s.clearMark(ctx, k); err != nil {
				return err
			}
		}
	}

	// Removed models of recent callers.
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT user_id, model FROM request_logs WHERE started_at >= $1 AND user_id IS NOT NULL
		AND status_code < 400 AND model <> ''`, now.AddDate(0, 0, -30))
	if err != nil {
		return err
	}
	type pair struct {
		user  uuid.UUID
		model string
	}
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.user, &p.model); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	usable := map[uuid.UUID]map[string]int{}
	live := map[string]bool{}
	for _, p := range pairs {
		ms, ok := usable[p.user]
		if !ok {
			ms = s.opts.Models.Models(p.user, nil)
			usable[p.user] = ms
		}
		key := modelGoneKeyPrefix + p.user.String() + ":" + p.model
		live[key] = true
		if ms[p.model] > 0 {
			if marks[key] {
				if err := s.clearMark(ctx, key); err != nil {
					return err
				}
			}
			continue
		}
		if marks[key] {
			continue
		}
		fresh, err := s.setMark(ctx, key, map[string]any{"at": now})
		if err != nil {
			return err
		}
		if !fresh || first {
			continue
		}
		s.emitLogged(ctx, Event{Type: TypeModelRemoved, Key: fmt.Sprintf("model.removed:%s:%s:%d", p.user, p.model, now.Unix()),
			Subject: p.model, Users: []uuid.UUID{p.user}, Title: "模型 " + p.model + " 已不可用",
			Body: "你近 30 天调用过的模型 " + p.model + " 现在不可用（平台下线或共享被收回），继续调用会返回“模型不存在”。",
			Link: "/console/my-models", Data: map[string]any{"model": p.model}})
	}
	// Marks of pairs that left the 30-day window are no longer needed.
	for k := range marks {
		if strings.HasPrefix(k, modelGoneKeyPrefix) && !live[k] {
			if err := s.clearMark(ctx, k); err != nil {
				return err
			}
		}
	}
	if first {
		return s.putState(ctx, stateModelsInit, true)
	}
	return nil
}

func (s *Service) activeUsers(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM users WHERE status = 'active' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return collectIDs(rows)
}

// Retention deletes in-app notifications older than 90 days and finished
// outbox rows older than 30 days, events older than 90 days (with their
// remaining rows) and stale verification codes. It runs with the daily
// request-log retention job.
func (s *Service) Retention(ctx context.Context, now time.Time) {
	for _, q := range []struct {
		sql    string
		cutoff time.Time
	}{
		{`DELETE FROM notifications WHERE created_at < $1`, now.AddDate(0, 0, -retentionDays)},
		{`DELETE FROM notification_deliveries WHERE status IN ('sent', 'failed', 'skipped', 'merged') AND updated_at < $1`, now.AddDate(0, 0, -30)},
		{`DELETE FROM notification_events WHERE created_at < $1`, now.AddDate(0, 0, -retentionDays)},
		{`DELETE FROM notification_email_codes WHERE created_at < $1`, now.AddDate(0, 0, -1)},
	} {
		if _, err := s.pool.Exec(ctx, q.sql, q.cutoff); err != nil && ctx.Err() == nil {
			s.log.Error("notification retention failed", "err", err)
		}
	}
}
