package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/netguard"
)

// ActionPreferencesUpdate is the audit action of a preferences change.
const ActionPreferencesUpdate = "notifications.preferences_update"

// Defaults (§3).
const (
	DefaultTimezone  = "Asia/Shanghai"
	defaultThreshold = money.Amount(1_000_000_000) // 1.00
	maxWebhookURL    = 2048
)

// Webhook formats (§3).
var webhookFormats = []string{"json", "feishu", "dingtalk", "wecom", "slack"}

// Switches are the per-event channel switches.
type Switches struct {
	Email   bool `json:"email"`
	Webhook bool `json:"webhook"`
	InApp   bool `json:"inApp"`
}

// EmailPrefs is the email section of the preferences.
type EmailPrefs struct {
	Enabled  bool    `json:"enabled"`
	Address  *string `json:"address"`
	Verified bool    `json:"verified"`
}

// WebhookPrefs is the webhook section of the preferences.
type WebhookPrefs struct {
	Enabled   bool    `json:"enabled"`
	URL       *string `json:"url"`
	SecretSet bool    `json:"secretSet"`
	Format    string  `json:"format"`
}

// Thresholds of threshold-based events.
type Thresholds struct {
	WalletBalanceLow string `json:"walletBalanceLow"`
}

// Preferences is the API document of GET/PUT /api/notifications/preferences.
type Preferences struct {
	Email      EmailPrefs          `json:"email"`
	Webhook    WebhookPrefs        `json:"webhook"`
	Events     map[string]Switches `json:"events"`
	Thresholds Thresholds          `json:"thresholds"`
	Digest     string              `json:"digest"`
	Timezone   string              `json:"timezone"`
	Version    int                 `json:"version"`
	// SMTPConfigured is read-only (§6.1): false when SMTP is not configured
	// or notifications are switched off.
	SMTPConfigured bool `json:"smtpConfigured"`
}

// stored is a preferences row (or the defaults when there is none).
type stored struct {
	UserID         uuid.UUID
	EmailEnabled   bool
	EmailAddress   *string // verified custom address
	WebhookEnabled bool
	WebhookURL     *string
	WebhookFormat  string
	Events         map[string]Switches // explicit switches; missing types use the defaults
	Threshold      money.Amount
	Digest         string
	Timezone       string
	Version        int // 0 = no row
}

func defaultStored(uid uuid.UUID) *stored {
	return &stored{UserID: uid, EmailEnabled: true, WebhookFormat: "json", Events: map[string]Switches{}, Threshold: defaultThreshold,
		Digest: "off", Timezone: DefaultTimezone}
}

// switches returns the effective switches of event type t.
func (p *stored) switches(t string) Switches {
	m, _ := Meta(t)
	sw, ok := p.Events[t]
	if !ok {
		sw = Switches{Email: m.Email, InApp: m.InApp}
	}
	if m.InAppLocked {
		sw.InApp = true
	}
	return sw
}

func (p *stored) location() *time.Location {
	if loc, err := time.LoadLocation(p.Timezone); err == nil {
		return loc
	}
	loc, _ := time.LoadLocation(DefaultTimezone)
	if loc == nil {
		return time.UTC
	}
	return loc
}

const prefCols = `user_id, email_enabled, email_address, webhook_enabled, webhook_url, webhook_format, events,
	wallet_threshold_nano, digest, timezone, version`

func scanPrefs(row db.Row) (*stored, error) {
	p := &stored{}
	var events []byte
	var thr int64
	if err := row.Scan(&p.UserID, &p.EmailEnabled, &p.EmailAddress, &p.WebhookEnabled, &p.WebhookURL, &p.WebhookFormat, &events,
		&thr, &p.Digest, &p.Timezone, &p.Version); err != nil {
		return nil, err
	}
	p.Threshold = money.Amount(thr)
	p.Events = map[string]Switches{}
	_ = json.Unmarshal(events, &p.Events)
	return p, nil
}

// loadPrefs returns the stored preferences of uid (defaults when none).
func (s *Service) loadPrefs(ctx context.Context, q db.Querier, uid uuid.UUID, lock bool) (*stored, error) {
	sql := `SELECT ` + prefCols + ` FROM notification_preferences WHERE user_id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	p, err := scanPrefs(q.QueryRow(ctx, sql, uid))
	if db.IsNoRows(err) {
		return defaultStored(uid), nil
	}
	return p, err
}

// loadPrefsMany returns preferences for every id (defaults when none).
func (s *Service) loadPrefsMany(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*stored, error) {
	out := make(map[uuid.UUID]*stored, len(ids))
	for _, id := range ids {
		out[id] = defaultStored(id)
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+prefCols+` FROM notification_preferences WHERE user_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPrefs(rows)
		if err != nil {
			return nil, err
		}
		out[p.UserID] = p
	}
	return out, rows.Err()
}

// savePrefs inserts or updates p (inside tx) and returns the new version.
func savePrefs(ctx context.Context, tx db.Tx, p *stored, now time.Time) (int, error) {
	events, _ := json.Marshal(p.Events)
	var v int
	err := tx.QueryRow(ctx, `INSERT INTO notification_preferences (user_id, email_enabled, email_address, webhook_enabled, webhook_url,
			webhook_format, events, wallet_threshold_nano, digest, timezone, version, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, $11)
		ON CONFLICT (user_id) DO UPDATE SET email_enabled = EXCLUDED.email_enabled, email_address = EXCLUDED.email_address,
			webhook_enabled = EXCLUDED.webhook_enabled, webhook_url = EXCLUDED.webhook_url, webhook_format = EXCLUDED.webhook_format,
			events = EXCLUDED.events, wallet_threshold_nano = EXCLUDED.wallet_threshold_nano, digest = EXCLUDED.digest,
			timezone = EXCLUDED.timezone, version = notification_preferences.version + 1, updated_at = EXCLUDED.updated_at
		RETURNING version`,
		p.UserID, p.EmailEnabled, p.EmailAddress, p.WebhookEnabled, p.WebhookURL, p.WebhookFormat, events, int64(p.Threshold),
		p.Digest, p.Timezone, now).Scan(&v)
	return v, err
}

// userInfo is what the notification code needs about a user.
type userInfo struct {
	ID            uuid.UUID
	Role          identity.Role
	Status        identity.Status
	Email         *string
	EmailVerified bool
}

const userInfoSQL = `SELECT u.id, u.role, u.status, u.email,
	EXISTS (SELECT 1 FROM user_identities i WHERE i.user_id = u.id AND i.email_verified AND lower(i.email) = lower(u.email))
	FROM users u`

func scanUser(row db.Row) (*userInfo, error) {
	u := &userInfo{}
	err := row.Scan(&u.ID, &u.Role, &u.Status, &u.Email, &u.EmailVerified)
	return u, err
}

func (s *Service) user(ctx context.Context, id uuid.UUID) (*userInfo, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, userInfoSQL+` WHERE u.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("用户")
	}
	return u, err
}

func (s *Service) users(ctx context.Context, ids []uuid.UUID) ([]*userInfo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, userInfoSQL+` WHERE u.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*userInfo
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// eligibility returns the eligibility facts of a user.
func (s *Service) eligibility(ctx context.Context, uid uuid.UUID, role identity.Role) (Eligibility, error) {
	e := Eligibility{Role: role}
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM channels WHERE owner_id = $1)`, uid).Scan(&e.OwnsChannel)
	return e, err
}

// view renders stored preferences for the API (only eligible events).
func (s *Service) view(ctx context.Context, p *stored, u *userInfo, el Eligibility) (*Preferences, error) {
	out := &Preferences{
		Email:      EmailPrefs{Enabled: p.EmailEnabled, Address: p.EmailAddress},
		Webhook:    WebhookPrefs{Enabled: p.WebhookEnabled, URL: p.WebhookURL, Format: p.WebhookFormat},
		Events:     map[string]Switches{},
		Thresholds: Thresholds{WalletBalanceLow: p.Threshold.String()},
		Digest:     p.Digest, Timezone: p.Timezone, Version: p.Version,
		SMTPConfigured: s.smtpUsable(ctx),
	}
	if p.EmailAddress != nil {
		out.Email.Verified = true
	} else {
		out.Email.Verified = u.Email != nil && *u.Email != "" && u.EmailVerified
	}
	for _, m := range Catalog {
		if el.Eligible(m.Type) {
			out.Events[m.Type] = p.switches(m.Type)
		}
	}
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notification_webhook_secrets WHERE user_id = $1)`, p.UserID).
		Scan(&out.Webhook.SecretSet)
	return out, err
}

// smtpUsable reports whether email can be sent (SMTP configured and
// notifications enabled).
func (s *Service) smtpUsable(ctx context.Context) bool {
	return s.set.NotificationsEnabled(ctx) && s.set.SMTP(ctx).Configured()
}

// GetPreferences returns the caller's preferences (defaults, version 0, when
// none were saved).
func (s *Service) GetPreferences(ctx context.Context, p *authz.Principal) (*Preferences, error) {
	u, err := s.user(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	el, err := s.eligibility(ctx, u.ID, u.Role)
	if err != nil {
		return nil, err
	}
	st, err := s.loadPrefs(ctx, s.pool, u.ID, false)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, st, u, el)
}

// PreferencesInput is the PUT body. Read-only fields are accepted and ignored.
type PreferencesInput struct {
	Email struct {
		Enabled  *bool   `json:"enabled"`
		Address  *string `json:"address"`
		Verified any     `json:"verified"`
	} `json:"email"`
	Webhook struct {
		Enabled   *bool   `json:"enabled"`
		URL       *string `json:"url"`
		SecretSet any     `json:"secretSet"`
		Format    *string `json:"format"`
	} `json:"webhook"`
	Events     map[string]Switches `json:"events"`
	Thresholds struct {
		WalletBalanceLow *string `json:"walletBalanceLow"`
	} `json:"thresholds"`
	Digest         *string `json:"digest"`
	Timezone       *string `json:"timezone"`
	Version        *int    `json:"version"`
	SMTPConfigured any     `json:"smtpConfigured"`
}

// RequestMeta carries request context for audit entries.
type RequestMeta struct {
	IPPrefix  string
	RequestID string
}

// checkWebhookURL validates a webhook URL; private targets follow the
// channel rule (allowed only with OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK for
// users holding channels.manage). The dial-time check stays authoritative.
func (s *Service) checkWebhookURL(raw string, role identity.Role) string {
	if len(raw) > maxWebhookURL {
		return fmt.Sprintf("地址过长（最多 %d 个字符）", maxWebhookURL)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "必须是 http(s) 开头的完整地址（不含用户信息和片段）"
	}
	if s.allowPrivate(role) {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "不允许指向内网或本机地址"
	}
	if ip, err := netip.ParseAddr(host); err == nil && !netguard.IsPublic(ip) {
		return "不允许指向内网或本机地址"
	}
	return ""
}

func (s *Service) allowPrivate(role identity.Role) bool {
	return s.opts.AllowPrivateNetwork && (&authz.Principal{Role: role}).Can(authz.ChannelsManage)
}

// PutPreferences replaces the caller's preferences (optimistic locking).
func (s *Service) PutPreferences(ctx context.Context, p *authz.Principal, in PreferencesInput, m RequestMeta) (*Preferences, error) {
	u, err := s.user(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	el, err := s.eligibility(ctx, u.ID, u.Role)
	if err != nil {
		return nil, err
	}
	details := map[string]any{}
	if in.Version == nil {
		details["version"] = "必填"
	}
	next := defaultStored(u.ID)
	if in.Email.Enabled != nil {
		next.EmailEnabled = *in.Email.Enabled
	}
	if in.Webhook.Enabled != nil {
		next.WebhookEnabled = *in.Webhook.Enabled
	}
	if in.Webhook.URL != nil {
		if v := strings.TrimSpace(*in.Webhook.URL); v != "" {
			if msg := s.checkWebhookURL(v, u.Role); msg != "" {
				details["webhook.url"] = msg
			}
			next.WebhookURL = &v
		}
	}
	if next.WebhookEnabled && next.WebhookURL == nil {
		details["webhook.url"] = "启用 Webhook 时必须填写地址"
	}
	if in.Webhook.Format != nil {
		next.WebhookFormat = *in.Webhook.Format
		if !contains(webhookFormats, next.WebhookFormat) {
			details["webhook.format"] = "只能是 " + strings.Join(webhookFormats, "、")
		}
	}
	for t, sw := range in.Events {
		if _, ok := Meta(t); !ok {
			details["events."+t] = "未知的事件类型"
			continue
		}
		if !el.Eligible(t) {
			if sw.Email || sw.Webhook || sw.InApp {
				details["events."+t] = "你没有资格接收该事件"
			}
			continue
		}
		if m, _ := Meta(t); m.InAppLocked {
			sw.InApp = true // cannot be switched off
		}
		next.Events[t] = sw
	}
	if v := in.Thresholds.WalletBalanceLow; v != nil {
		a, err := money.Parse(strings.TrimSpace(*v))
		if err != nil || a < 0 {
			details["thresholds.walletBalanceLow"] = "必须是不小于 0 的十进制金额（最多 9 位小数）"
		}
		next.Threshold = a
	}
	if in.Digest != nil {
		next.Digest = *in.Digest
		if next.Digest != "off" && next.Digest != "daily" {
			details["digest"] = "只能是 off 或 daily"
		}
	}
	if in.Timezone != nil {
		next.Timezone = strings.TrimSpace(*in.Timezone)
		if _, err := time.LoadLocation(next.Timezone); err != nil || next.Timezone == "" || next.Timezone == "Local" || len(next.Timezone) > 64 {
			details["timezone"] = "无效的时区（IANA 名称，如 Asia/Shanghai）"
		}
	}
	var view *Preferences
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		cur, err := s.loadPrefs(ctx, tx, u.ID, true)
		if err != nil {
			return err
		}
		// The custom address can only be set by verification (§3).
		if a := in.Email.Address; a != nil && strings.TrimSpace(*a) != "" {
			if cur.EmailAddress == nil || !strings.EqualFold(*cur.EmailAddress, strings.TrimSpace(*a)) {
				details["email.address"] = "请先验证该邮箱（发送验证码并确认）"
			} else {
				next.EmailAddress = cur.EmailAddress
			}
		}
		if len(details) > 0 {
			return apperr.Validation("通知设置校验失败", details)
		}
		if *in.Version != cur.Version {
			return apperr.VersionConflict()
		}
		v, err := savePrefs(ctx, tx, next, s.now())
		if err != nil {
			return err
		}
		next.Version = v
		rid := u.ID.String()
		return s.audit.Record(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: ActionPreferencesUpdate,
			ResourceType: "notification_preferences", ResourceID: &rid, IPPrefix: m.IPPrefix, RequestID: m.RequestID,
			Metadata: map[string]any{"changed": changedParts(cur, next), "version": v}})
	})
	if err != nil {
		return nil, err
	}
	s.forgetThreshold(u.ID)
	view, err = s.view(ctx, next, u, el)
	return view, err
}

// changedParts lists the changed sections (audit; no addresses or URLs).
func changedParts(a, b *stored) []string {
	var out []string
	add := func(c bool, f string) {
		if c {
			out = append(out, f)
		}
	}
	ae, _ := json.Marshal(a.Events)
	be, _ := json.Marshal(b.Events)
	add(string(ae) != string(be), "events")
	add(a.EmailEnabled != b.EmailEnabled || fmt.Sprint(a.EmailAddress) != fmt.Sprint(b.EmailAddress), "email")
	add(a.WebhookEnabled != b.WebhookEnabled || deref(a.WebhookURL) != deref(b.WebhookURL) || a.WebhookFormat != b.WebhookFormat, "webhook")
	add(a.Threshold != b.Threshold, "thresholds")
	add(a.Digest != b.Digest || a.Timezone != b.Timezone, "digest")
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---- wallet threshold cache (hot path: every settlement) ----

type cachedThreshold struct {
	amount money.Amount
	at     time.Time
}

const thresholdTTL = time.Minute

func (s *Service) walletThreshold(ctx context.Context, uid uuid.UUID) (money.Amount, error) {
	s.mu.Lock()
	c, ok := s.thresholds[uid]
	s.mu.Unlock()
	if ok && time.Since(c.at) < thresholdTTL {
		return c.amount, nil
	}
	var thr int64
	err := s.pool.QueryRow(ctx, `SELECT wallet_threshold_nano FROM notification_preferences WHERE user_id = $1`, uid).Scan(&thr)
	switch {
	case db.IsNoRows(err):
		thr = int64(defaultThreshold)
	case err != nil:
		return 0, err
	}
	s.mu.Lock()
	if len(s.thresholds) > 10000 {
		s.thresholds = map[uuid.UUID]cachedThreshold{}
	}
	s.thresholds[uid] = cachedThreshold{money.Amount(thr), time.Now()}
	s.mu.Unlock()
	return money.Amount(thr), nil
}

func (s *Service) forgetThreshold(uid uuid.UUID) {
	s.mu.Lock()
	delete(s.thresholds, uid)
	s.mu.Unlock()
}
