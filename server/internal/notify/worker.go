package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/platform/db"
)

const (
	claimBatch    = 50
	claimLease    = 5 * time.Minute
	emailAttempts = 5 // §6: exponential backoff, at most 5 attempts
	pollInterval  = 5 * time.Second
	summaryMax    = 50 // items listed in a digest / summary email
)

// delivery is a claimed outbox row.
type delivery struct {
	ID       uuid.UUID
	UserID   uuid.UUID
	EventID  *uuid.UUID
	Channel  string
	Kind     string
	Type     string
	Attempts int
	Created  time.Time
}

// eventRow is a stored notification event.
type eventRow struct {
	ID        uuid.UUID
	Type      string
	Title     string
	Body      string
	Link      *string
	Data      map[string]any
	CreatedAt time.Time
}

// Run delivers due notifications until ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		for {
			n, err := s.ProcessDue(ctx)
			if err != nil && ctx.Err() == nil {
				s.log.Error("notification delivery failed", "err", err)
			}
			if err != nil || n < claimBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

const deliveryCols = `id, user_id, event_id, channel, kind, type, attempts, created_at`

func collectDeliveries(rows db.Rows, err error) ([]delivery, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []delivery
	for rows.Next() {
		var d delivery
		if err := rows.Scan(&d.ID, &d.UserID, &d.EventID, &d.Channel, &d.Kind, &d.Type, &d.Attempts, &d.Created); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// claim leases due deliveries: FOR UPDATE SKIP LOCKED lets several
// PostgreSQL instances claim disjoint rows; on SQLite the transaction holds
// the database write lock (single instance). A claimed row is "sending" with
// a lease in next_attempt_at, so rows of a crashed worker are claimed again
// once the lease expires. Digest items of a user are always claimed together.
func (s *Service) claim(ctx context.Context, now time.Time) ([]delivery, error) {
	lease := now.Add(claimLease)
	var out []delivery
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var err error
		out, err = collectDeliveries(tx.Query(ctx, `UPDATE notification_deliveries SET status = 'sending', next_attempt_at = $2, updated_at = $1
			WHERE id IN (SELECT id FROM notification_deliveries WHERE status IN ('pending', 'sending') AND next_attempt_at <= $1
				ORDER BY next_attempt_at LIMIT $3 FOR UPDATE SKIP LOCKED)
			RETURNING `+deliveryCols, now, lease, claimBatch))
		if err != nil {
			return err
		}
		seen := map[uuid.UUID]bool{}
		for _, d := range out {
			if d.Kind != "digest" || seen[d.UserID] {
				continue
			}
			seen[d.UserID] = true
			more, err := collectDeliveries(tx.Query(ctx, `UPDATE notification_deliveries SET status = 'sending', next_attempt_at = $2, updated_at = $1
				WHERE id IN (SELECT id FROM notification_deliveries WHERE user_id = $3 AND kind = 'digest' AND status = 'pending'
					AND next_attempt_at <= $1 FOR UPDATE SKIP LOCKED)
				RETURNING `+deliveryCols, now, lease, d.UserID))
			if err != nil {
				return err
			}
			out = append(out, more...)
		}
		return nil
	})
	return out, err
}

// ProcessDue claims and processes one batch of due deliveries; it returns
// how many rows were claimed.
func (s *Service) ProcessDue(ctx context.Context) (int, error) {
	now := s.now()
	list, err := s.claim(ctx, now)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	// RETURNING does not keep the subquery order: oldest first.
	slices.SortStableFunc(list, func(a, b delivery) int { return a.Created.Compare(b.Created) })
	digests := map[uuid.UUID][]delivery{}
	for _, d := range list {
		switch {
		case d.Kind == "digest":
			digests[d.UserID] = append(digests[d.UserID], d)
		case d.Kind == "summary":
			s.processSummary(ctx, d)
		case d.Channel == "webhook":
			s.processWebhook(ctx, d)
		default:
			s.processEmail(ctx, d)
		}
	}
	for uid, ds := range digests {
		s.processDigest(ctx, uid, ds)
	}
	return len(list), nil
}

// ---- outcome bookkeeping ----

func (s *Service) markSent(ctx context.Context, ds []delivery) {
	now := s.now()
	for _, d := range ds {
		if _, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET status = 'sent', attempts = attempts + 1, sent_at = $2,
			updated_at = $2, last_error = NULL WHERE id = $1`, d.ID, now); err != nil {
			s.log.Error("mark delivery sent", "err", err)
		}
		metricSent.WithLabelValues(d.Channel, d.Type, resultSent).Inc()
	}
}

func (s *Service) markSkipped(ctx context.Context, ds []delivery, reason string) {
	now := s.now()
	for _, d := range ds {
		if _, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET status = 'skipped', last_error = $2, updated_at = $3 WHERE id = $1`,
			d.ID, reason, now); err != nil {
			s.log.Error("mark delivery skipped", "err", err)
		}
		metricSent.WithLabelValues(d.Channel, d.Type, resultSkipped).Inc()
	}
}

// markFailure schedules a retry (or gives up) after a failed attempt.
func (s *Service) markFailure(ctx context.Context, ds []delivery, reason string) {
	now := s.now()
	for _, d := range ds {
		n := d.Attempts + 1
		status, next, result := "pending", now, resultRetry
		if d.Channel == "webhook" {
			if n > len(webhookRetries) {
				status, result = "failed", resultFailed
			} else {
				next = now.Add(webhookRetries[n-1])
			}
		} else {
			if n >= emailAttempts {
				status, result = "failed", resultFailed
			} else {
				next = now.Add(time.Minute << (n - 1))
			}
		}
		if _, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET status = $2, attempts = $3, next_attempt_at = $4, last_error = $5,
			updated_at = $6 WHERE id = $1`, d.ID, status, n, next, truncate(reason, 500), now); err != nil {
			s.log.Error("mark delivery failed", "err", err)
		}
		metricSent.WithLabelValues(d.Channel, d.Type, result).Inc()
	}
}

func (s *Service) loadEvents(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*eventRow, error) {
	out := map[uuid.UUID]*eventRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id, type, title, body, link, data, created_at FROM notification_events WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		e := &eventRow{}
		var data []byte
		if err := rows.Scan(&e.ID, &e.Type, &e.Title, &e.Body, &e.Link, &data, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(data, &e.Data)
		out[e.ID] = e
	}
	return out, rows.Err()
}

// recipient is the sending context of one user.
type recipient struct {
	user  *userInfo
	prefs *stored
	email string // verified address ("" = none)
}

func (s *Service) recipient(ctx context.Context, uid uuid.UUID) (*recipient, error) {
	u, err := s.user(ctx, uid)
	if err != nil {
		return nil, err
	}
	p, err := s.loadPrefs(ctx, s.pool, uid, false)
	if err != nil {
		return nil, err
	}
	r := &recipient{user: u, prefs: p}
	switch {
	case p.EmailAddress != nil:
		r.email = *p.EmailAddress
	case u.Email != nil && u.EmailVerified:
		r.email = *u.Email
	}
	return r, nil
}

// emailBlocked returns why email cannot go to r for type t ("" = allowed).
func (s *Service) emailBlocked(ctx context.Context, r *recipient, t string) string {
	switch {
	case r.user.Status != "active" && !ReachesDisabled(t):
		return "用户已停用"
	case !s.smtpUsable(ctx):
		return "SMTP 未配置或通知已关闭"
	case !r.prefs.EmailEnabled || (t != "" && !r.prefs.switches(t).Email):
		return "用户已关闭该邮件通知"
	case r.email == "":
		return "没有已验证的邮箱地址"
	}
	return ""
}

func (s *Service) settingsURL() string { return s.link("/console/notifications/settings") }

func (s *Service) unsubscribeURL(uid uuid.UUID, t string) string {
	tok, err := s.UnsubscribeToken(uid, t)
	if err != nil {
		return ""
	}
	return s.link("/api/notifications/unsubscribe?token=" + url.QueryEscape(tok))
}

func (s *Service) item(e *eventRow) mailItem {
	return mailItem{Title: e.Title, Body: e.Body, Link: s.link(deref(e.Link)), CreatedAt: e.CreatedAt}
}

func (s *Service) mail(ctx context.Context, to string, subject string, v mailView, unsub string) error {
	v.Site = s.set.SiteName(ctx)
	v.SettingsURL = s.settingsURL()
	v.Unsubscribe = unsub
	if v.UnsubLabel == "" {
		v.UnsubLabel = "退订此类邮件"
	}
	h, t := render(v)
	return sendMail(ctx, s.set.SMTP(ctx), s.opts.Production, s.opts.SMTPTLS,
		Mail{To: to, Subject: "[" + v.Site + "] " + subject, HTML: h, Text: t, Unsubscribe: unsub})
}

// ---- email ----

func (s *Service) processEmail(ctx context.Context, d delivery) {
	r, err := s.recipient(ctx, d.UserID)
	if err != nil {
		s.markFailure(ctx, []delivery{d}, "读取用户失败")
		return
	}
	if why := s.emailBlocked(ctx, r, d.Type); why != "" {
		s.markSkipped(ctx, []delivery{d}, why)
		return
	}
	if limited, err := s.overLimit(ctx, d); err != nil {
		s.markFailure(ctx, []delivery{d}, "检查发送频率失败")
		return
	} else if limited {
		return
	}
	evs, err := s.loadEvents(ctx, []uuid.UUID{*d.EventID})
	if err != nil || evs[*d.EventID] == nil {
		s.markSkipped(ctx, []delivery{d}, "事件不存在")
		return
	}
	e := evs[*d.EventID]
	v := mailView{Heading: e.Title, Items: []mailItem{s.item(e)}, Loc: r.prefs.location()}
	if err := s.mail(ctx, r.email, e.Title, v, s.unsubscribeURL(d.UserID, d.Type)); err != nil {
		s.markFailure(ctx, []delivery{d}, err.Error())
		return
	}
	s.markSent(ctx, []delivery{d})
}

// overLimit enforces notifications.emailRateLimitPerHour: beyond the limit
// the delivery is held and one summary email is scheduled for when the
// hourly window frees up. Digest emails do not count (one per day).
func (s *Service) overLimit(ctx context.Context, d delivery) (bool, error) {
	limit := s.set.EmailRateLimitPerHour(ctx)
	if limit <= 0 {
		return false, nil
	}
	now := s.now()
	var n int
	var oldest *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT count(*), min(sent_at) FROM notification_deliveries
		WHERE user_id = $1 AND channel = 'email' AND kind IN ('event', 'summary') AND status = 'sent' AND sent_at > $2`,
		d.UserID, now.Add(-time.Hour)).Scan(&n, &oldest); err != nil {
		return false, err
	}
	if n < limit {
		return false, nil
	}
	due := now.Add(time.Hour)
	if oldest != nil {
		due = oldest.Add(time.Hour)
	}
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE notification_deliveries SET status = 'held', attempts = attempts + 1, updated_at = $2,
			last_error = '超过每小时邮件上限，将合并为摘要' WHERE id = $1`, d.ID, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO notification_deliveries (id, user_id, channel, kind, type, status, next_attempt_at, created_at, updated_at)
			VALUES ($1, $2, 'email', 'summary', 'summary', 'pending', $3, $4, $4) ON CONFLICT DO NOTHING`, newID(), d.UserID, due, now)
		return err
	})
	if err == nil {
		metricSent.WithLabelValues("email", d.Type, resultLimited).Inc()
	}
	return err == nil, err
}

// processSummary sends the held (rate-limited) notifications of a user as one email.
func (s *Service) processSummary(ctx context.Context, d delivery) {
	r, err := s.recipient(ctx, d.UserID)
	if err != nil {
		s.markFailure(ctx, []delivery{d}, "读取用户失败")
		return
	}
	held, err := collectDeliveries(s.pool.Query(ctx, `SELECT `+deliveryCols+` FROM notification_deliveries
		WHERE user_id = $1 AND channel = 'email' AND status = 'held' ORDER BY created_at`, d.UserID))
	if err != nil {
		s.markFailure(ctx, []delivery{d}, "读取待合并通知失败")
		return
	}
	if len(held) == 0 {
		s.markSkipped(ctx, []delivery{d}, "没有待合并的通知")
		return
	}
	if why := s.emailBlocked(ctx, r, ""); why != "" {
		s.markSkipped(ctx, append(held, d), why)
		return
	}
	items, err := s.items(ctx, held)
	if err != nil {
		s.markFailure(ctx, []delivery{d}, "读取事件失败")
		return
	}
	intro := fmt.Sprintf("过去一小时内的通知超过了每小时 %d 封的上限，以下 %d 条已合并为这封摘要。", s.set.EmailRateLimitPerHour(ctx), len(held))
	v := mailView{Heading: fmt.Sprintf("%d 条通知摘要", len(held)), Intro: intro, Items: items, Loc: r.prefs.location(),
		UnsubLabel: "关闭全部邮件通知"}
	if err := s.mail(ctx, r.email, v.Heading, v, s.unsubscribeURL(d.UserID, "")); err != nil {
		s.markFailure(ctx, []delivery{d}, err.Error())
		return
	}
	now := s.now()
	for _, h := range held {
		if _, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET status = 'merged', sent_at = $2, updated_at = $2 WHERE id = $1`,
			h.ID, now); err != nil {
			s.log.Error("mark delivery merged", "err", err)
		}
	}
	s.markSent(ctx, []delivery{d})
}

// items renders the events of ds (at most summaryMax, newest last).
func (s *Service) items(ctx context.Context, ds []delivery) ([]mailItem, error) {
	var ids []uuid.UUID
	for _, d := range ds {
		if d.EventID != nil {
			ids = append(ids, *d.EventID)
		}
	}
	evs, err := s.loadEvents(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []mailItem
	for _, id := range ids {
		if e := evs[id]; e != nil {
			out = append(out, s.item(e))
		}
	}
	if extra := len(out) - summaryMax; extra > 0 {
		out = append(out[:summaryMax], mailItem{Title: "更多通知", Body: fmt.Sprintf("另有 %d 条通知，请到站内通知中心查看。", extra),
			Link: s.link("/console/notifications"), CreatedAt: s.now()})
	}
	return out, nil
}

// processDigest sends a user's due digest items as one email (§3 digest).
func (s *Service) processDigest(ctx context.Context, uid uuid.UUID, ds []delivery) {
	r, err := s.recipient(ctx, uid)
	if err != nil {
		s.markFailure(ctx, ds, "读取用户失败")
		return
	}
	var keep, drop []delivery
	for _, d := range ds {
		if why := s.emailBlocked(ctx, r, d.Type); why != "" {
			drop = append(drop, d)
		} else {
			keep = append(keep, d)
		}
	}
	if len(drop) > 0 {
		s.markSkipped(ctx, drop, "用户已关闭该邮件通知或没有可用邮箱")
	}
	if len(keep) == 0 {
		return
	}
	items, err := s.items(ctx, keep)
	if err != nil {
		s.markFailure(ctx, keep, "读取事件失败")
		return
	}
	loc := r.prefs.location()
	day := s.now().In(loc).Format("2006-01-02")
	v := mailView{Heading: "每日通知摘要 · " + day, Intro: fmt.Sprintf("以下是过去一天的 %d 条通知。", len(keep)), Items: items, Loc: loc,
		UnsubLabel: "关闭全部邮件通知"}
	if err := s.mail(ctx, r.email, v.Heading, v, s.unsubscribeURL(uid, "")); err != nil {
		s.markFailure(ctx, keep, err.Error())
		return
	}
	s.markSent(ctx, keep)
}

// ---- webhook ----

func (s *Service) webhookSecret(ctx context.Context, uid uuid.UUID) (string, error) {
	var ct string
	err := s.pool.QueryRow(ctx, `SELECT ciphertext FROM notification_webhook_secrets WHERE user_id = $1`, uid).Scan(&ct)
	if db.IsNoRows(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	pt, err := s.secretBox.Open(ct, secretAD(uid))
	return string(pt), err
}

func secretAD(uid uuid.UUID) string { return "notification_webhook_secret:" + uid.String() }

func (s *Service) payload(e *eventRow) Payload {
	data := e.Data
	if data == nil {
		data = map[string]any{}
	}
	return Payload{ID: e.ID.String(), Type: e.Type, Title: e.Title, Body: e.Body, URL: s.link(deref(e.Link)), Data: data,
		CreatedAt: e.CreatedAt.UTC()}
}

func (s *Service) processWebhook(ctx context.Context, d delivery) {
	r, err := s.recipient(ctx, d.UserID)
	if err != nil {
		s.markFailure(ctx, []delivery{d}, "读取用户失败")
		return
	}
	p := r.prefs
	if (r.user.Status != "active" && !ReachesDisabled(d.Type)) || !s.set.NotificationsEnabled(ctx) || !p.WebhookEnabled || p.WebhookURL == nil || !p.switches(d.Type).Webhook {
		s.markSkipped(ctx, []delivery{d}, "Webhook 已关闭")
		return
	}
	secret, err := s.webhookSecret(ctx, d.UserID)
	if err != nil {
		s.markSkipped(ctx, []delivery{d}, "Webhook 密钥无法读取（主密钥可能已更换），请重新设置")
		return
	}
	evs, err := s.loadEvents(ctx, []uuid.UUID{*d.EventID})
	if err != nil || evs[*d.EventID] == nil {
		s.markSkipped(ctx, []delivery{d}, "事件不存在")
		return
	}
	res := s.postWebhook(ctx, *p.WebhookURL, p.WebhookFormat, secret, s.allowPrivate(r.user.Role), s.payload(evs[*d.EventID]))
	if !res.OK {
		s.markFailure(ctx, []delivery{d}, deref(res.Error))
		return
	}
	s.markSent(ctx, []delivery{d})
}

// TestWebhook sends a test message to the caller's configured webhook.
func (s *Service) TestWebhook(ctx context.Context, uid uuid.UUID) (*WebhookResult, error) {
	r, err := s.recipient(ctx, uid)
	if err != nil {
		return nil, err
	}
	if r.prefs.WebhookURL == nil {
		return nil, validation("webhook.url", "请先保存 Webhook 地址")
	}
	secret, err := s.webhookSecret(ctx, uid)
	if err != nil {
		return nil, err
	}
	site := s.set.SiteName(ctx)
	p := Payload{ID: newID().String(), Type: "test", Title: site + " Webhook 测试",
		Body: "这是一条测试消息。收到它说明 Webhook 配置正确。", URL: s.settingsURL(), Data: map[string]any{"test": true}, CreatedAt: s.now()}
	res := s.postWebhook(ctx, strings.TrimSpace(*r.prefs.WebhookURL), r.prefs.WebhookFormat, secret, s.allowPrivate(r.user.Role), p)
	return &res, nil
}
