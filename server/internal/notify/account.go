package notify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/platform/db"
)

const (
	codeTTL          = 10 * time.Minute
	codesPerHour     = 5
	codeMaxAttempts  = 5
	maxWebhookSecret = 256
)

func validation(field, msg string) error {
	return apperr.Validation("参数校验失败", map[string]any{field: msg})
}

// ErrSMTPUnavailable is the 409 returned when SMTP is not configured (§6.1).
func ErrSMTPUnavailable() *apperr.Error {
	return apperr.New(apperr.KindConflict, "smtp_not_configured", "邮件发送未配置（SMTP），请联系管理员")
}

func normalizeAddress(raw string) (string, error) {
	a := strings.TrimSpace(raw)
	if a == "" || len(a) > 254 {
		return "", validation("address", "请输入邮箱地址")
	}
	p, err := mail.ParseAddress(a)
	if err != nil || p.Name != "" || p.Address != a || !strings.Contains(a[strings.LastIndexByte(a, '@')+1:], ".") {
		return "", validation("address", "邮箱地址格式不正确")
	}
	return a, nil
}

func codeHash(uid uuid.UUID, address, code string) string {
	h := sha256.Sum256([]byte(uid.String() + "\x00" + strings.ToLower(address) + "\x00" + code))
	return hex.EncodeToString(h[:])
}

// SendVerification emails a 6-digit code to address (10-minute validity, at
// most 5 per user and hour). Only a hash of the code is stored.
func (s *Service) SendVerification(ctx context.Context, p *authz.Principal, raw string) (time.Time, error) {
	address, err := normalizeAddress(raw)
	if err != nil {
		return time.Time{}, err
	}
	if !s.smtpUsable(ctx) {
		return time.Time{}, ErrSMTPUnavailable()
	}
	now := s.now()
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notification_email_codes WHERE user_id = $1 AND created_at > $2`,
		p.UserID, now.Add(-time.Hour)).Scan(&n); err != nil {
		return time.Time{}, err
	}
	if n >= codesPerHour {
		return time.Time{}, apperr.New(apperr.KindRateLimited, "rate_limited", "验证码发送过于频繁（每小时最多 5 次），请稍后再试")
	}
	num, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return time.Time{}, err
	}
	code := fmt.Sprintf("%06d", num.Int64())
	exp := now.Add(codeTTL)
	if _, err := s.pool.Exec(ctx, `INSERT INTO notification_email_codes (id, user_id, address, code_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, newID(), p.UserID, address, codeHash(p.UserID, address, code), exp, now); err != nil {
		return time.Time{}, err
	}
	site := s.set.SiteName(ctx)
	v := mailView{Site: site, Heading: "验证你的通知邮箱", Code: code,
		Intro: "你正在将此邮箱设为 " + site + " 的通知邮箱。验证码 10 分钟内有效；如果不是你本人操作，请忽略这封邮件。"}
	h, t := render(v)
	if err := sendMail(ctx, s.set.SMTP(ctx), s.opts.Production, s.opts.SMTPTLS,
		Mail{To: address, Subject: "[" + site + "] 通知邮箱验证码", HTML: h, Text: t}); err != nil {
		return time.Time{}, &apperr.Error{Kind: apperr.KindUpstream, Code: "email_send_failed", Message: "验证码邮件发送失败：" + err.Error(), Err: err}
	}
	return exp, nil
}

// ConfirmVerification checks the code and makes address the notification email.
func (s *Service) ConfirmVerification(ctx context.Context, p *authz.Principal, raw, code string, m RequestMeta) (*Preferences, error) {
	address, err := normalizeAddress(raw)
	if err != nil {
		return nil, err
	}
	code = strings.TrimSpace(code)
	now := s.now()
	var verr error
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var id uuid.UUID
		var hash, requested string
		var attempts int
		var exp time.Time
		err := tx.QueryRow(ctx, `SELECT id, address, code_hash, attempts, expires_at FROM notification_email_codes
			WHERE user_id = $1 AND lower(address) = lower($2) AND used_at IS NULL ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
			p.UserID, address).Scan(&id, &requested, &hash, &attempts, &exp)
		if db.IsNoRows(err) || (err == nil && (!now.Before(exp) || attempts >= codeMaxAttempts)) {
			verr = apperr.New(apperr.KindValidation, "verification_expired", "验证码已过期或不存在，请重新获取")
			return nil
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(hash), []byte(codeHash(p.UserID, address, code))) != 1 {
			if _, err := tx.Exec(ctx, `UPDATE notification_email_codes SET attempts = attempts + 1 WHERE id = $1`, id); err != nil {
				return err
			}
			verr = apperr.New(apperr.KindValidation, "verification_invalid", "验证码不正确")
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE notification_email_codes SET used_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		cur, err := s.loadPrefs(ctx, tx, p.UserID, true)
		if err != nil {
			return err
		}
		cur.EmailAddress = &requested // the address the code was sent to
		v, err := savePrefs(ctx, tx, cur, now)
		if err != nil {
			return err
		}
		rid := p.UserID.String()
		return s.audit.Record(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: ActionPreferencesUpdate,
			ResourceType: "notification_preferences", ResourceID: &rid, IPPrefix: m.IPPrefix, RequestID: m.RequestID,
			Metadata: map[string]any{"changed": []string{"email.address"}, "version": v}})
	})
	if err != nil {
		return nil, err
	}
	if verr != nil {
		return nil, verr
	}
	return s.GetPreferences(ctx, p)
}

// SetWebhookSecret stores (or with "" clears) the caller's webhook HMAC secret.
func (s *Service) SetWebhookSecret(ctx context.Context, p *authz.Principal, secret string, m RequestMeta) (bool, error) {
	if len(secret) > maxWebhookSecret || strings.ContainsAny(secret, "\r\n") {
		return false, validation("secret", fmt.Sprintf("不能超过 %d 个字符，且不能包含换行", maxWebhookSecret))
	}
	now := s.now()
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if secret == "" {
			if _, err := tx.Exec(ctx, `DELETE FROM notification_webhook_secrets WHERE user_id = $1`, p.UserID); err != nil {
				return err
			}
		} else {
			ct, err := s.secretBox.Seal([]byte(secret), secretAD(p.UserID))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO notification_webhook_secrets (user_id, ciphertext, updated_at) VALUES ($1, $2, $3)
				ON CONFLICT (user_id) DO UPDATE SET ciphertext = EXCLUDED.ciphertext, updated_at = EXCLUDED.updated_at`,
				p.UserID, ct, now); err != nil {
				return err
			}
		}
		rid := p.UserID.String()
		return s.audit.Record(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: ActionPreferencesUpdate,
			ResourceType: "notification_preferences", ResourceID: &rid, IPPrefix: m.IPPrefix, RequestID: m.RequestID,
			Metadata: map[string]any{"changed": []string{"webhook.secret"}, "secretSet": secret != ""}})
	})
	return secret != "", err
}

// SMTPTest sends a test email with the current SMTP settings (§1).
func (s *Service) SMTPTest(ctx context.Context, to string) (bool, string, error) {
	address, err := normalizeAddress(to)
	if err != nil {
		return false, "", apperr.Validation("参数校验失败", map[string]any{"to": "邮箱地址格式不正确"})
	}
	cfg := s.set.SMTP(ctx)
	if !cfg.Configured() {
		return false, "", ErrSMTPUnavailable()
	}
	site := s.set.SiteName(ctx)
	v := mailView{Site: site, Heading: "SMTP 测试邮件", Intro: "这是一封来自 " + site + " 的测试邮件。收到它说明邮件发送配置正确。",
		SettingsURL: s.link("/console/admin/settings")}
	h, t := render(v)
	if err := sendMail(ctx, cfg, s.opts.Production, s.opts.SMTPTLS, Mail{To: address, Subject: "[" + site + "] SMTP 测试邮件", HTML: h, Text: t}); err != nil {
		return false, err.Error(), nil
	}
	return true, "", nil
}

// Unsubscribe applies a one-click unsubscribe token (idempotent). It returns
// the event type that was turned off ("" = all email notifications).
func (s *Service) Unsubscribe(ctx context.Context, token string) (string, error) {
	t, err := s.parseUnsubscribe(token)
	if err != nil {
		return "", apperr.New(apperr.KindValidation, "invalid_token", "退订链接无效")
	}
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, t.U).Scan(&exists); err != nil || !exists {
			if err == nil {
				err = apperr.New(apperr.KindValidation, "invalid_token", "退订链接无效")
			}
			return err
		}
		cur, err := s.loadPrefs(ctx, tx, t.U, true)
		if err != nil {
			return err
		}
		if t.T == "" {
			if !cur.EmailEnabled {
				return nil
			}
			cur.EmailEnabled = false
		} else {
			sw := cur.switches(t.T)
			if !sw.Email {
				return nil
			}
			sw.Email = false
			cur.Events[t.T] = sw
		}
		v, err := savePrefs(ctx, tx, cur, s.now())
		if err != nil {
			return err
		}
		rid := t.U.String()
		return s.audit.Record(ctx, tx, audit.Entry{ActorID: &t.U, Action: ActionPreferencesUpdate,
			ResourceType: "notification_preferences", ResourceID: &rid,
			Metadata: map[string]any{"changed": []string{"events"}, "via": "unsubscribe", "type": t.T, "version": v}})
	})
	return t.T, err
}

// ---- in-app notifications (§4) ----

// Notification is the API form of an in-app notification.
type Notification struct {
	ID        uuid.UUID      `json:"id"`
	Type      string         `json:"type"`
	Severity  string         `json:"severity"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	Link      *string        `json:"link"`
	Data      map[string]any `json:"data"`
	ReadAt    *time.Time     `json:"readAt"`
	CreatedAt time.Time      `json:"createdAt"`
}

// ListQuery filters the caller's notifications.
type ListQuery struct {
	Unread bool
	Types  []string // exact types; empty = all
	// TypePrefixes match type prefixes (alerts summary).
	TypePrefixes []string
	Offset       int
	Limit        int
}

// List returns the caller's notifications, newest first.
func (s *Service) List(ctx context.Context, uid uuid.UUID, q ListQuery) ([]Notification, int, error) {
	where := ` WHERE n.user_id = $1`
	args := []any{uid}
	if q.Unread {
		where += ` AND n.read_at IS NULL`
	}
	if len(q.Types) > 0 {
		args = append(args, q.Types)
		where += fmt.Sprintf(` AND n.type = ANY($%d)`, len(args))
	}
	if len(q.TypePrefixes) > 0 {
		var ors []string
		for _, p := range q.TypePrefixes {
			args = append(args, p+"%")
			ors = append(ors, fmt.Sprintf(`n.type LIKE $%d`, len(args)))
		}
		where += ` AND (` + strings.Join(ors, " OR ") + `)`
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications n`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := s.pool.Query(ctx, `SELECT n.id, n.type, n.severity, e.title, e.body, e.link, e.data, n.read_at, n.created_at
		FROM notifications n JOIN notification_events e ON e.id = n.event_id`+where+
		fmt.Sprintf(` ORDER BY n.created_at DESC, n.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var data []byte
		if err := rows.Scan(&n.ID, &n.Type, &n.Severity, &n.Title, &n.Body, &n.Link, &data, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, 0, err
		}
		n.Data = map[string]any{}
		_ = json.Unmarshal(data, &n.Data)
		n.CreatedAt = n.CreatedAt.UTC()
		if n.ReadAt != nil {
			t := n.ReadAt.UTC()
			n.ReadAt = &t
		}
		out = append(out, n)
	}
	return out, total, rows.Err()
}

// UnreadCount returns the number of unread notifications.
func (s *Service) UnreadCount(ctx context.Context, uid uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`, uid).Scan(&n)
	return n, err
}

// MarkRead marks ids (or every notification when all) as read.
func (s *Service) MarkRead(ctx context.Context, uid uuid.UUID, ids []uuid.UUID, all bool) (int64, error) {
	now := s.now()
	if all {
		tag, err := s.pool.Exec(ctx, `UPDATE notifications SET read_at = $2 WHERE user_id = $1 AND read_at IS NULL`, uid, now)
		if err != nil {
			return 0, err
		}
		return tag.RowsAffected(), nil
	}
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE notifications SET read_at = $3 WHERE user_id = $1 AND id = ANY($2) AND read_at IS NULL`, uid, ids, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
