package notify

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/authz"
	"omnigate/internal/billing"
	"omnigate/internal/channel"
	"omnigate/internal/identity"
	"omnigate/internal/limits"
	"omnigate/internal/plugin"
	"omnigate/internal/pricing"
	"omnigate/internal/subscription"
	"omnigate/internal/usergroup"
)

// Hooks connect the producers to the other services. Every hook returns
// immediately; the event is emitted in the background.

const authFailedWindow = 6 * time.Hour

func (s *Service) currency() string {
	if s.opts.Currency == "" {
		return ""
	}
	return " " + s.opts.Currency
}

// usersWithPerm returns the active users whose role grants perm.
func (s *Service) usersWithPerm(ctx context.Context, perm authz.Permission) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM users WHERE status = 'active' AND role = ANY($1) ORDER BY id`, rolesWith(perm))
	if err != nil {
		return nil, err
	}
	return collectIDs(rows)
}

type idRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

func collectIDs(rows idRows) ([]uuid.UUID, error) {
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

// ---- wallet (billing.OnLedger) ----

// WalletChanged handles a committed ledger change: credits and the
// low-balance crossing with re-arm (§2).
func (s *Service) WalletChanged(_ context.Context, c billing.LedgerChange) {
	q := &s.walletQ
	q.mu.Lock()
	if q.pending == nil {
		q.pending, q.running, q.last = map[uuid.UUID][]billing.LedgerChange{}, map[uuid.UUID]bool{}, map[uuid.UUID]uuid.UUID{}
	}
	q.pending[c.UserID] = append(q.pending[c.UserID], c)
	if q.running[c.UserID] {
		q.mu.Unlock()
		return
	}
	q.running[c.UserID] = true
	q.mu.Unlock()
	s.goAsync(func(ctx context.Context) { s.drainWallet(ctx, c.UserID) })
}

// walletQueue holds per-user wallet events waiting to be handled.
type walletQueue struct {
	mu      sync.Mutex
	pending map[uuid.UUID][]billing.LedgerChange
	running map[uuid.UUID]bool
	last    map[uuid.UUID]uuid.UUID // newest ledger entry handled per user
}

// drainWallet handles a user's queued wallet events one at a time.
func (s *Service) drainWallet(ctx context.Context, user uuid.UUID) {
	q := &s.walletQ
	for {
		q.mu.Lock()
		list := q.pending[user]
		if len(list) == 0 {
			delete(q.pending, user)
			delete(q.running, user)
			q.mu.Unlock()
			return
		}
		c := list[0]
		q.pending[user] = list[1:]
		// Ledger entry ids are UUIDv7 (time ordered). Post-commit callbacks of
		// concurrent transactions can arrive out of order; an event older than
		// one already handled must not re-arm or set the low-balance mark.
		last := q.last[user]
		stale := bytes.Compare(c.EntryID[:], last[:]) < 0
		if !stale {
			q.last[user] = c.EntryID
		}
		q.mu.Unlock()
		s.walletChanged(ctx, c, stale)
	}
}

func (s *Service) walletChanged(ctx context.Context, c billing.LedgerChange, stale bool) {
	if c.Amount > 0 && (c.Kind == billing.KindGrant || c.Kind == billing.KindAdjust) {
		reason := map[string]string{billing.RefRedeem: "兑换码充值", billing.RefSignup: "新用户赠送", billing.RefAdmin: "管理员调整"}[c.RefType]
		if reason == "" {
			reason = "入账"
		}
		s.emitLogged(ctx, Event{Type: TypeWalletCredited, Key: "wallet.credited:" + c.EntryID.String(), Users: []uuid.UUID{c.UserID},
			Title: fmt.Sprintf("钱包入账 %s%s", c.Amount, s.currency()),
			Body:  fmt.Sprintf("%s：+%s%s，当前余额 %s%s。", reason, c.Amount, s.currency(), c.BalanceAfter, s.currency()),
			Link:  "/console/billing", Data: map[string]any{"amount": c.Amount.String(), "balance": c.BalanceAfter.String(), "reason": c.RefType}})
	}
	if stale {
		return
	}
	thr, err := s.walletThreshold(ctx, c.UserID)
	if err != nil {
		s.log.Error("wallet threshold", "err", err)
		return
	}
	mark := "wallet_low:" + c.UserID.String()
	avail := c.BalanceAfter - c.Reserved
	if avail >= thr {
		if c.Amount > 0 {
			if err := s.clearMark(ctx, mark); err != nil {
				s.log.Error("wallet low re-arm", "err", err)
			}
		}
		return
	}
	if c.BalanceBefore < thr {
		return // was already below: not a crossing
	}
	fresh, err := s.setMark(ctx, mark, map[string]any{"at": c.At})
	if err != nil || !fresh {
		return
	}
	s.emitLogged(ctx, Event{Type: TypeWalletBalanceLow, Key: "wallet.balance_low:" + c.UserID.String() + ":" + strconv.FormatInt(c.At.UnixNano(), 10),
		Users: []uuid.UUID{c.UserID}, Title: "钱包余额不足",
		Body: fmt.Sprintf("你的可用余额为 %s%s，已低于提醒阈值 %s%s。请及时充值，以免请求因余额不足被拒绝。", avail, s.currency(), thr, s.currency()),
		Link: "/console/billing", Data: map[string]any{"available": avail.String(), "threshold": thr.String()}})
}

// ---- quota (subscription.OnQuota) ----

// QuotaReached handles a rule at ≥ 80 % (or 100 %) of its window.
func (s *Service) QuotaReached(_ context.Context, q subscription.QuotaState) {
	s.goAsync(func(ctx context.Context) {
		t, title := TypeQuotaNearLimit, "套餐额度即将用完"
		body := fmt.Sprintf("套餐「%s」的额度「%s」本周期已用 %s / %s（超过 80%%）。", q.PlanName, q.RuleLabel, q.Used, q.Limit)
		if q.Exhausted {
			t, title = TypeQuotaExhausted, "套餐额度已用完"
			body = fmt.Sprintf("套餐「%s」的额度「%s」本周期已用完（%s / %s）。", q.PlanName, q.RuleLabel, q.Used, q.Limit)
		}
		if q.ResetsAt != nil {
			body += "额度将于 " + q.ResetsAt.UTC().Format("2006-01-02 15:04 UTC") + " 重置。"
		}
		data := map[string]any{"subscriptionId": q.SubscriptionID, "ruleId": q.RuleID, "used": q.Used, "limit": q.Limit, "planName": q.PlanName}
		if q.ResetsAt != nil {
			data["resetsAt"] = q.ResetsAt.UTC()
		}
		s.emitLogged(ctx, Event{Type: t, Key: t + ":" + q.SubscriptionID.String() + ":" + q.RuleID + ":" + q.WindowKey,
			Users: []uuid.UUID{q.UserID}, Title: title, Body: body, Link: "/console/billing", Data: data})
	})
}

// ---- prices (pricing.OnSellPrice) ----

// SellPriceChanged notifies users who called the model on the platform in
// the last 30 days about a new sell price (also future-effective ones).
func (s *Service) SellPriceChanged(_ context.Context, prev, next *pricing.Price) {
	if next == nil || pricing.SamePrice(prev, next) {
		return
	}
	s.goAsync(func(ctx context.Context) {
		users, err := s.recentCallers(ctx, next.Model)
		if err != nil {
			s.log.Error("price change recipients", "err", err)
			return
		}
		f := func(p *pricing.Price) string {
			if p == nil {
				return "未定价"
			}
			return fmt.Sprintf("输入 %s / 输出 %s（每百万 tokens）", p.InputPerM, p.OutputPerM)
		}
		when := "已生效"
		if next.EffectiveAt.After(s.now().Add(time.Minute)) {
			when = "将于 " + next.EffectiveAt.UTC().Format("2006-01-02 15:04 UTC") + " 生效"
		}
		body := fmt.Sprintf("模型 %s 的售价调整%s：%s → %s。", next.Model, when, f(prev), f(next))
		if next.PerRequest > 0 || (prev != nil && prev.PerRequest > 0) {
			body += fmt.Sprintf("按次费用 %s。", next.PerRequest)
		}
		data := map[string]any{"model": next.Model, "effectiveAt": next.EffectiveAt.UTC(), "inputPerM": next.InputPerM.String(),
			"outputPerM": next.OutputPerM.String(), "perRequest": next.PerRequest.String()}
		if prev != nil {
			data["previous"] = map[string]any{"inputPerM": prev.InputPerM.String(), "outputPerM": prev.OutputPerM.String(), "perRequest": prev.PerRequest.String()}
		}
		s.emitLogged(ctx, Event{Type: TypeModelPriceChanged, Key: "model.price_changed:" + next.ID.String(), Subject: next.Model, Users: users,
			Title: "模型 " + next.Model + " 价格调整", Body: body, Link: "/console/plaza", Data: data})
	})
}

// recentCallers returns users with a successful platform-tier request for
// model in the last 30 days.
func (s *Service) recentCallers(ctx context.Context, model string) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT user_id FROM request_logs WHERE model = $1 AND started_at >= $2
		AND user_id IS NOT NULL AND status_code < 400 AND coalesce(channel_tier, 'platform') = 'platform'`,
		model, s.now().AddDate(0, 0, -30))
	if err != nil {
		return nil, err
	}
	return collectIDs(rows)
}

// ---- plugins (plugin.OnPendingApproval) ----

// PluginPending notifies plugins.trust administrators of a version awaiting approval.
func (s *Service) PluginPending(_ context.Context, v plugin.PendingVersion) {
	s.goAsync(func(ctx context.Context) {
		users, err := s.usersWithPerm(ctx, authz.PluginsTrust)
		if err != nil {
			s.log.Error("plugin approval recipients", "err", err)
			return
		}
		s.emitLogged(ctx, Event{Type: TypePluginPendingApproval, Key: "plugin.pending_approval:" + v.VersionID.String(), Users: users,
			Title: fmt.Sprintf("插件 %s %s 等待审批", v.Name, v.Version),
			Body:  fmt.Sprintf("%s 发布了插件 %s（%s）的新版本 %s，新增了需要审批的权限。", v.Publisher, v.Name, v.Key, v.Version),
			Link:  "/console/plugins/" + v.PluginID.String(),
			Data:  map[string]any{"pluginId": v.PluginID, "versionId": v.VersionID, "key": v.Key, "version": v.Version}})
	})
}

// ---- channels (breaker, registry, capabilities) ----

type channelInfo struct {
	ID        uuid.UUID
	Name      string
	Owner     uuid.UUID
	Platform  bool
	Threshold *string
}

func (s *Service) channelInfo(ctx context.Context, id uuid.UUID) (*channelInfo, error) {
	c := &channelInfo{ID: id}
	var role identity.Role
	if err := s.pool.QueryRow(ctx, `SELECT c.name, c.owner_id, u.role, c.alert_balance_below FROM channels c JOIN users u ON u.id = c.owner_id
		WHERE c.id = $1`, id).Scan(&c.Name, &c.Owner, &role, &c.Threshold); err != nil {
		return nil, err
	}
	c.Platform = channel.IsPlatformRole(role)
	return c, nil
}

// channelRecipients: the owner; for platform channels also every
// channels.manage administrator (§2). Users a channel is shared with are
// never notified.
func (s *Service) channelRecipients(ctx context.Context, c *channelInfo) ([]uuid.UUID, error) {
	users := []uuid.UUID{c.Owner}
	if c.Platform {
		admins, err := s.usersWithPerm(ctx, authz.ChannelsManage)
		if err != nil {
			return nil, err
		}
		users = append(users, admins...)
	}
	return users, nil
}

func (s *Service) emitChannel(ctx context.Context, id uuid.UUID, build func(c *channelInfo) Event) {
	c, err := s.channelInfo(ctx, id)
	if err != nil {
		return // channel deleted meanwhile
	}
	users, err := s.channelRecipients(ctx, c)
	if err != nil {
		s.log.Error("channel event recipients", "err", err)
		return
	}
	ev := build(c)
	ev.Users, ev.Link, ev.Subject = users, "/console/channels/"+c.ID.String(), c.ID.String()
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	ev.Data["channelId"], ev.Data["channelName"] = c.ID, c.Name
	s.emitLogged(ctx, ev)
}

// ChannelTransition handles circuit breaker transitions (open / recovered).
func (s *Service) ChannelTransition(id uuid.UUID, unhealthy bool, lastErr string) {
	at := s.now()
	s.goAsync(func(ctx context.Context) {
		s.emitChannel(ctx, id, func(c *channelInfo) Event {
			if unhealthy {
				body := "渠道「" + c.Name + "」连续失败，熔断已打开，请求会暂时绕过该渠道。"
				if lastErr != "" {
					body += "\n最近错误：" + truncate(lastErr, 300)
				}
				return Event{Type: TypeChannelUnhealthy, Key: "channel.unhealthy:" + c.ID.String() + ":" + strconv.FormatInt(at.Unix(), 10),
					Title: "渠道「" + c.Name + "」异常", Body: body, Data: map[string]any{"lastError": lastErr}}
			}
			return Event{Type: TypeChannelRecovered, Key: "channel.recovered:" + c.ID.String() + ":" + strconv.FormatInt(at.Unix(), 10),
				Title: "渠道「" + c.Name + "」已恢复", Body: "渠道「" + c.Name + "」请求恢复成功，已重新参与路由。"}
		})
	})
}

// ChannelAuthFailed handles an upstream 401/403: at most one notification
// per channel within 6 hours (in-memory pre-check, then the dedupe bucket and
// a database throttle across instances).
func (s *Service) ChannelAuthFailed(id uuid.UUID, status int) {
	now := s.now()
	s.mu.Lock()
	if last, ok := s.authSeen[id]; ok && now.Sub(last) < authFailedWindow {
		s.mu.Unlock()
		return
	}
	s.authSeen[id] = now
	s.mu.Unlock()
	bucket := now.Unix() / int64(authFailedWindow/time.Second)
	s.goAsync(func(ctx context.Context) {
		s.emitChannel(ctx, id, func(c *channelInfo) Event {
			return Event{Type: TypeChannelAuthFailed, Key: fmt.Sprintf("channel.auth_failed:%s:%d", c.ID, bucket), Throttle: authFailedWindow,
				Title: "渠道「" + c.Name + "」凭据失效",
				Body:  fmt.Sprintf("上游对渠道「%s」返回 HTTP %d，API Key 可能已失效、被禁用或权限不足，请检查并更新凭据。", c.Name, status),
				Data:  map[string]any{"statusCode": status}}
		})
	})
}

// BalanceRead compares a fresh upstream balance with the channel threshold
// (alerts.balanceBelow, in the plugin's currency); it re-arms once the
// balance is back at or above the threshold.
func (s *Service) BalanceRead(_ context.Context, r channel.BalanceReading) {
	s.goAsync(func(ctx context.Context) {
		c, err := s.channelInfo(ctx, r.ChannelID)
		if err != nil {
			return
		}
		mark := "upstream_low:" + c.ID.String()
		if !channel.BalanceLow(r.Total, c.Threshold) {
			if err := s.clearMark(ctx, mark); err != nil {
				s.log.Error("upstream balance re-arm", "err", err)
			}
			return
		}
		fresh, err := s.setMark(ctx, mark, map[string]any{"total": r.Total, "currency": r.Currency})
		if err != nil || !fresh {
			return
		}
		s.emitChannel(ctx, c.ID, func(c *channelInfo) Event {
			return Event{Type: TypeUpstreamBalanceLow, Key: "upstream.balance_low:" + c.ID.String() + ":" + strconv.FormatInt(r.FetchedAt.UnixNano(), 10),
				Title: "渠道「" + c.Name + "」上游余额不足",
				Body:  fmt.Sprintf("渠道「%s」的上游余额为 %s %s，已低于告警阈值 %s %s。", c.Name, r.Total, r.Currency, *c.Threshold, r.Currency),
				Data:  map[string]any{"total": r.Total, "currency": r.Currency, "threshold": *c.Threshold, "available": r.Available}}
		})
	})
}

// ---- accounts (adminapi.OnStatusChanged; phase7-api.md §2) ----

// AccountStatusChanged tells a user that an administrator disabled or enabled
// the account or signed out every session (or the suspension ended). It is
// delivered even though the user may be disabled; the in-app copy cannot be
// switched off.
func (s *Service) AccountStatusChanged(_ context.Context, c identity.StatusChange) {
	s.goAsync(func(ctx context.Context) {
		data := map[string]any{"action": c.Action, "auto": c.Auto}
		var title, body string
		severity := SeverityInfo
		switch c.Action {
		case identity.ChangeDisabled:
			title, severity = "你的账号已被停用", SeverityWarn
			body = "管理员停用了你的账号，登录和 API Key 调用将被拒绝。"
			if c.Reason != nil && *c.Reason != "" {
				body += "\n原因：" + *c.Reason
				data["reason"] = *c.Reason
			}
			if c.Until != nil {
				body += "\n将于 " + c.Until.UTC().Format("2006-01-02 15:04 UTC") + " 自动恢复。"
				data["until"] = c.Until.UTC()
			} else {
				body += "\n停用没有期限，需管理员手动启用。"
			}
		case identity.ChangeEnabled:
			title, body = "你的账号已恢复", "管理员已启用你的账号，可以正常登录和调用 API。"
			if c.Auto {
				body = "账号停用期已结束，账号已自动恢复，可以正常登录和调用 API。"
			}
		case identity.ChangeLogout:
			title, severity = "你已被强制下线", SeverityWarn
			body = "管理员结束了你的全部登录会话，请重新登录。API Key 不受影响。"
		default:
			return
		}
		s.emitLogged(ctx, Event{Type: TypeAccountStatusChanged,
			Key:   fmt.Sprintf("account.status_changed:%s:%s:%d", c.UserID, c.Action, c.At.UnixNano()),
			Users: []uuid.UUID{c.UserID}, Severity: severity, Title: title, Body: body, Link: "/console", Data: data})
	})
}

// ---- subscriptions (subscription.OnBulk; phase7-api.md §3) ----

// SubscriptionBulk notifies every affected user of a quota reset or an
// extension: one notification per user and operation.
func (s *Service) SubscriptionBulk(_ context.Context, n subscription.BulkNotice) {
	s.goAsync(func(ctx context.Context) {
		var order []uuid.UUID
		byUser := map[uuid.UUID][]subscription.BulkItem{}
		for _, it := range n.Items {
			if _, ok := byUser[it.UserID]; !ok {
				order = append(order, it.UserID)
			}
			byUser[it.UserID] = append(byUser[it.UserID], it)
		}
		for _, uid := range order {
			items := byUser[uid]
			subs := make([]map[string]any, len(items))
			var lines []string
			for i, it := range items {
				subs[i] = map[string]any{"subscriptionId": it.SubscriptionID, "planName": it.PlanName, "endsAt": it.EndsAt.UTC()}
				if n.IsReset() {
					subs[i]["rules"] = it.Rules
					lines = append(lines, fmt.Sprintf("「%s」：%s", it.PlanName, strings.Join(it.Rules, "、")))
				} else {
					lines = append(lines, fmt.Sprintf("「%s」：有效期延长至 %s", it.PlanName, it.EndsAt.UTC().Format("2006-01-02 15:04 UTC")))
				}
			}
			t, title := TypeSubscriptionQuotaReset, "套餐额度已重置"
			body := "管理员重置了你订阅的套餐额度，当前周期的用量已清零：\n" + strings.Join(lines, "\n")
			if !n.IsReset() {
				t, title = TypeSubscriptionExtended, "订阅已延期"
				body = "管理员将你的订阅延长了 " + n.Duration + "：\n" + strings.Join(lines, "\n")
			}
			data := map[string]any{"subscriptions": subs, "note": n.Note}
			if !n.IsReset() {
				data["duration"] = n.Duration
			}
			if n.Note != "" {
				body += "\n备注：" + n.Note
			}
			s.emitLogged(ctx, Event{Type: t, Key: t + ":" + n.OperationID.String() + ":" + uid.String(), Users: []uuid.UUID{uid},
				Title: title, Body: body, Link: "/console/billing", Data: data})
		}
	})
}

// ---- channel shares (phase5-api.md §5.5) ----

// shareInviteWindow: at most one invitation notification per channel and
// recipient within this window (repeated re-invitations stay silent).
const shareInviteWindow = time.Hour

// ShareInvited tells users that a channel was shared with them and waits for
// their acceptance, with the privacy implications of accepting.
func (s *Service) ShareInvited(_ context.Context, inv channel.ShareInvite) {
	s.goAsync(func(ctx context.Context) {
		models := strings.Join(inv.Models, "、")
		if len([]rune(models)) > 200 {
			models = string([]rune(models)[:200]) + "…"
		}
		body := fmt.Sprintf("%s 想把渠道「%s」共享给你（模型：%s）。接受后，这些模型的请求会先经过该渠道（排在平台渠道之前、不计费）。\n"+
			"注意：渠道所有者看不到你的账户信息，但能看到经由该渠道发送的请求与响应内容。只接受你信任的人的共享，之后可以随时退出。",
			inv.Owner.DisplayName, inv.ChannelName, models)
		for _, uid := range inv.Users {
			s.emitLogged(ctx, Event{Type: TypeChannelShareInvited,
				Key:     fmt.Sprintf("channel.share_invited:%s:%s:%d", inv.ChannelID, uid, inv.At.UnixNano()),
				Subject: inv.ChannelID.String() + ":" + uid.String(), Throttle: shareInviteWindow,
				Users: []uuid.UUID{uid}, Title: fmt.Sprintf("%s 邀请你使用渠道「%s」", inv.Owner.DisplayName, inv.ChannelName),
				Body: body, Link: "/console/channels?tab=shared",
				Data: map[string]any{"channelId": inv.ChannelID, "channelName": inv.ChannelName,
					"owner": map[string]any{"id": inv.Owner.ID, "displayName": inv.Owner.DisplayName}, "models": inv.Models}})
		}
	})
}

// ---- user groups and limits (phase8-api.md §1.3, §2.2) ----

// AccountGroupChanged tells a user that an administrator moved them to
// another user group (or their group was deleted).
func (s *Service) AccountGroupChanged(_ context.Context, c usergroup.Change) {
	s.goAsync(func(ctx context.Context) {
		to := c.To
		body := fmt.Sprintf("管理员将你移到了用户组「%s」。", to.Name)
		if c.Deleted {
			body = fmt.Sprintf("你所在的用户组「%s」已被删除，你已移到默认用户组「%s」。", c.From.Name, to.Name)
		}
		if to.Multiplier != pricing.One {
			body += fmt.Sprintf("\n平台渠道价格倍率：×%s。", to.Multiplier)
		}
		var lims []string
		if l := to.Limits; l.RPM != nil || l.RPD != nil || l.DailySpend != nil || l.MonthlySpend != nil {
			if l.RPM != nil {
				lims = append(lims, fmt.Sprintf("每分钟 %d 次请求", *l.RPM))
			}
			if l.RPD != nil {
				lims = append(lims, fmt.Sprintf("每天 %d 次请求", *l.RPD))
			}
			if l.DailySpend != nil {
				lims = append(lims, fmt.Sprintf("每天消费 %s%s", *l.DailySpend, s.currency()))
			}
			if l.MonthlySpend != nil {
				lims = append(lims, fmt.Sprintf("每月消费 %s%s", *l.MonthlySpend, s.currency()))
			}
			body += "\n限额：" + strings.Join(lims, "、") + "。"
		}
		s.emitLogged(ctx, Event{Type: TypeAccountGroupChanged,
			Key:   fmt.Sprintf("account.group_changed:%s:%s:%d", c.UserID, to.ID, c.At.UnixNano()),
			Users: []uuid.UUID{c.UserID}, Title: "你的用户组已变更为「" + to.Name + "」", Body: body, Link: "/console",
			Data: map[string]any{"from": c.From, "to": usergroup.Ref{ID: to.ID, Name: to.Name}, "priceMultiplier": to.Multiplier.String(),
				"limits": to.Limits.JSON(), "deleted": c.Deleted}})
	})
}

// SpendLimit handles a spend limit at ≥ 80 % (limit.spend_near) or reached
// (limit.spend_reached): at most one of each per limit and window.
func (s *Service) SpendLimit(_ context.Context, e limits.SpendEvent) {
	s.goAsync(func(ctx context.Context) {
		var what, subject string
		switch e.Kind {
		case limits.KindDaily:
			what, subject = "用户组每日消费限额", "user:"+e.UserID.String()+":daily"
		case limits.KindMonthly:
			what, subject = "用户组每月消费限额", "user:"+e.UserID.String()+":monthly"
		default:
			what = "API Key「" + e.KeyName + "」的消费限额"
			subject = "key:" + e.KeyID.String() + ":" + string(e.Window)
		}
		t, title := TypeLimitSpendNear, what+"即将用完"
		body := fmt.Sprintf("%s已使用 %s / %s%s（超过 80%%）。", what, e.Spent, e.Limit, s.currency())
		if e.Reached {
			t, title = TypeLimitSpendReached, what+"已用完"
			body = fmt.Sprintf("%s已用完（%s / %s%s），平台渠道的请求将被拒绝（429 spend_limit_exceeded）；自有与共享渠道不受影响。",
				what, e.Spent, e.Limit, s.currency())
		}
		data := map[string]any{"kind": e.Kind, "window": e.Window, "spent": e.Spent.String(), "limit": e.Limit.String(),
			"windowStart": e.WindowStart.UTC()}
		if e.ResetsAt != nil {
			body += "限额将于 " + e.ResetsAt.UTC().Format("2006-01-02 15:04 UTC") + " 重置。"
			data["resetsAt"] = e.ResetsAt.UTC()
		}
		if e.KeyID != nil {
			data["keyId"], data["keyName"] = *e.KeyID, e.KeyName
		}
		link := "/console/billing"
		if e.KeyID != nil {
			link = "/console/keys"
		}
		s.emitLogged(ctx, Event{Type: t, Key: fmt.Sprintf("%s:%s:%d", t, subject, e.WindowStart.Unix()),
			Users: []uuid.UUID{e.UserID}, Title: title, Body: body, Link: link, Data: data})
	})
}
