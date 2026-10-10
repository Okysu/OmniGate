// Package settings implements runtime-editable system settings
// (docs/contracts/phase4-api.md §3).
//
// Values resolve as database → environment → default. Database overrides are
// stored as one JSON document (flat keys such as "site.name") under
// system_settings key 'system', whose row version is the optimistic-lock
// version of all settings. Reads are cached for 5 seconds; writes on this
// instance refresh the cache immediately.
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/affinity"
	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/secretbox"
	"omnigate/internal/routing"
)

// ActionUpdate is the audit action of a settings change.
const ActionUpdate = "settings.update"

// ActionSMTPUpdate is recorded (next to settings.update) when any
// notifications.smtp.* field changes (docs/contracts/phase6-api.md §6).
const ActionSMTPUpdate = "notifications.smtp_update"

// keySMTPPassword is the write-only SMTP password; the settings document
// stores it sealed with the master key (ADR-0008).
const keySMTPPassword = "notifications.smtp.password"

const (
	rowKey = "system"
	// legacyEnforceKey is the pre-Round 5 billing.enforce row; its value seeds
	// the settings document once.
	legacyEnforceKey = "billing.enforce"
	// CacheTTL bounds how stale another instance's change can be.
	CacheTTL = 5 * time.Second
)

// Sources of an effective value.
const (
	SourceDB      = "db"
	SourceEnv     = "env"
	SourceDefault = "default"
)

type Site struct {
	Name           string `json:"name"`
	Announcement   string `json:"announcement"`
	LandingEnabled bool   `json:"landingEnabled"`
	DocsURL        string `json:"docsUrl"`
	// PublicModelPlaza lets anonymous visitors open the platform model plaza
	// (GET /api/plaza/models, docs/contracts/phase5-api.md §3.1).
	PublicModelPlaza bool `json:"publicModelPlaza"`
}

type Auth struct {
	RegistrationMode    string   `json:"registrationMode"`
	AllowedEmailDomains []string `json:"allowedEmailDomains"`
}

type Billing struct {
	Enforce      bool   `json:"enforce"`
	SignupCredit string `json:"signupCredit"`
	// Referral rebates (phase15-api.md §4.1): on/off, the percentage of an
	// invitee's wallet_credit recharge and the minimum recharge that earns one.
	ReferralEnabled     bool   `json:"referralEnabled"`
	ReferralRate        string `json:"referralRate"`
	ReferralMinRecharge string `json:"referralMinRecharge"`
}

type Gateway struct {
	MaxAttempts      int      `json:"maxAttempts"`
	RetryOn          []string `json:"retryOn"`
	LogRetentionDays int      `json:"logRetentionDays"`
	// Affinity is the session affinity setting (phase12-api.md §1; new-api's
	// snake_case JSON shape).
	Affinity affinity.Config `json:"affinity"`
}

// SMTP is the read view of notifications.smtp (the password is write-only).
type SMTP struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"passwordSet"`
	From        string `json:"from"`
}

// Notifications is the notifications group (docs/contracts/phase6-api.md §1).
type Notifications struct {
	SMTP                  SMTP `json:"smtp"`
	Enabled               bool `json:"enabled"`
	EmailRateLimitPerHour int  `json:"emailRateLimitPerHour"`
}

// Settings is the effective (resolved) system settings.
type Settings struct {
	Site          Site          `json:"site"`
	Auth          Auth          `json:"auth"`
	Billing       Billing       `json:"billing"`
	Gateway       Gateway       `json:"gateway"`
	Notifications Notifications `json:"notifications"`
}

// SMTPConfig is the effective SMTP configuration including the password
// (server-side use only).
type SMTPConfig struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
}

// Configured reports whether email can be sent at all.
func (c SMTPConfig) Configured() bool { return c.Host != "" && c.From != "" }

// field describes one setting. Values are canonical Go values: string, bool,
// int64, []string or (gateway.affinity) affinity.Config.
type field struct {
	key    string
	def    any
	decode func(json.RawMessage) (any, string) // canonical value or an error message
}

var fields = []field{
	{"site.name", "OmniGate", decodeString(1, 50, nil)},
	{"site.announcement", "", decodeString(0, 500, nil)},
	{"site.landingEnabled", true, decodeBool},
	{"site.docsUrl", "", decodeString(0, 500, validDocsURL)},
	{"site.publicModelPlaza", true, decodeBool},
	{"auth.registrationMode", "restricted", decodeEnum("open", "restricted", "closed")},
	{"auth.allowedEmailDomains", []string{}, decodeDomains},
	{"billing.enforce", false, decodeBool},
	{"billing.signupCredit", "0", decodeMoney},
	{"billing.referralEnabled", false, decodeBool},
	{"billing.referralRate", "10", decodeRate},
	{"billing.referralMinRecharge", "0", decodeMoney},
	{"gateway.maxAttempts", int64(3), decodeInt(1, 5)},
	{"gateway.retryOn", slices.Clone(routing.DefaultRetryClasses), decodeRetryOn},
	{"gateway.logRetentionDays", int64(90), decodeInt(0, 3650)},
	{"gateway.affinity", affinity.Default(), decodeAffinity},
	{"notifications.smtp.host", "", decodeString(0, 253, validSMTPHost)},
	{"notifications.smtp.port", int64(587), decodeInt(1, 65535)},
	{"notifications.smtp.security", "starttls", decodeEnum("starttls", "tls", "none")},
	{"notifications.smtp.username", "", decodeString(0, 256, nil)},
	{keySMTPPassword, "", decodePassword},
	{"notifications.smtp.from", "", decodeString(0, 320, validFrom)},
	{"notifications.enabled", true, decodeBool},
	{"notifications.emailRateLimitPerHour", int64(20), decodeInt(1, 1000)},
}

func validSMTPHost(s string) string {
	if strings.ContainsAny(s, " /@\t") {
		return "必须是主机名或 IP 地址（不含协议和端口）"
	}
	return ""
}

func validFrom(s string) string {
	if s == "" {
		return ""
	}
	if _, err := mail.ParseAddress(s); err != nil {
		return "必须是合法的邮件地址，如 OmniGate <noreply@example.com>"
	}
	return ""
}

// decodePassword validates a new password (plaintext; "" clears it).
func decodePassword(raw json.RawMessage) (any, string) {
	var p string
	if json.Unmarshal(raw, &p) != nil {
		return nil, "必须是字符串"
	}
	if len(p) > 512 || strings.ContainsAny(p, "\r\n") {
		return nil, "不能超过 512 个字符，且不能包含换行"
	}
	return p, ""
}

func fieldByKey(k string) (field, bool) {
	for _, f := range fields {
		if f.key == k {
			return f, true
		}
	}
	return field{}, false
}

func decodeString(minLen, maxLen int, check func(string) string) func(json.RawMessage) (any, string) {
	return func(raw json.RawMessage) (any, string) {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, "必须是字符串"
		}
		s = strings.TrimSpace(s)
		if n := len([]rune(s)); n < minLen || n > maxLen {
			if minLen == 0 {
				return nil, fmt.Sprintf("不能超过 %d 个字符", maxLen)
			}
			return nil, fmt.Sprintf("长度应为 %d–%d 个字符", minLen, maxLen)
		}
		if check != nil {
			if msg := check(s); msg != "" {
				return nil, msg
			}
		}
		return s, ""
	}
}

func validDocsURL(s string) string {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "必须为空或 http(s) URL"
	}
	return ""
}

func decodeBool(raw json.RawMessage) (any, string) {
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return nil, "必须是布尔值"
	}
	return b, ""
}

func decodeEnum(values ...string) func(json.RawMessage) (any, string) {
	return func(raw json.RawMessage) (any, string) {
		var s string
		if json.Unmarshal(raw, &s) != nil || !slices.Contains(values, s) {
			return nil, "只能是 " + strings.Join(values, "、")
		}
		return s, ""
	}
}

func decodeInt(minV, maxV int64) func(json.RawMessage) (any, string) {
	return func(raw json.RawMessage) (any, string) {
		var n int64
		if json.Unmarshal(raw, &n) != nil || n < minV || n > maxV {
			return nil, fmt.Sprintf("必须是 %d–%d 的整数", minV, maxV)
		}
		return n, ""
	}
}

func decodeMoney(raw json.RawMessage) (any, string) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil, "必须是十进制字符串"
	}
	a, err := money.Parse(strings.TrimSpace(s))
	if err != nil || a < 0 {
		return nil, "必须是非负十进制数，最多 9 位小数"
	}
	return a.String(), ""
}

func decodeRetryOn(raw json.RawMessage) (any, string) {
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return nil, "必须是字符串数组"
	}
	out := []string{}
	for _, c := range list {
		if !slices.Contains(routing.AllRetryClasses, c) {
			return nil, "只能包含 " + strings.Join(routing.AllRetryClasses, "、")
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, ""
}

func decodeAffinity(raw json.RawMessage) (any, string) {
	c, msg := affinity.Decode(raw)
	if msg != "" {
		return nil, msg
	}
	return c, ""
}

func decodeDomains(raw json.RawMessage) (any, string) {
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return nil, "必须是字符串数组"
	}
	if len(list) > 100 {
		return nil, "最多 100 个域名"
	}
	out := []string{}
	for _, d := range list {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || len(d) > 253 || strings.ContainsAny(d, "@/ :") || !strings.Contains(d, ".") {
			return nil, fmt.Sprintf("%q 不是合法的域名", d)
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out, ""
}

// values are effective settings keyed by field key.
type values map[string]any

func (v values) settings() Settings {
	str := func(k string) string { s, _ := v[k].(string); return s }
	boolean := func(k string) bool { b, _ := v[k].(bool); return b }
	num := func(k string) int64 { n, _ := v[k].(int64); return n }
	domains, _ := v["auth.allowedEmailDomains"].([]string)
	retryOn, _ := v["gateway.retryOn"].([]string)
	aff, _ := v["gateway.affinity"].(affinity.Config)
	return Settings{
		Site: Site{Name: str("site.name"), Announcement: str("site.announcement"), LandingEnabled: boolean("site.landingEnabled"), DocsURL: str("site.docsUrl"),
			PublicModelPlaza: boolean("site.publicModelPlaza")},
		Auth: Auth{RegistrationMode: str("auth.registrationMode"), AllowedEmailDomains: slices.Clone(domains)},
		Billing: Billing{Enforce: boolean("billing.enforce"), SignupCredit: str("billing.signupCredit"),
			ReferralEnabled: boolean("billing.referralEnabled"), ReferralRate: str("billing.referralRate"),
			ReferralMinRecharge: str("billing.referralMinRecharge")},
		Gateway: Gateway{MaxAttempts: int(num("gateway.maxAttempts")), RetryOn: slices.Clone(retryOn), LogRetentionDays: int(num("gateway.logRetentionDays")),
			Affinity: aff},
		Notifications: Notifications{
			SMTP: SMTP{Host: str("notifications.smtp.host"), Port: int(num("notifications.smtp.port")), Security: str("notifications.smtp.security"),
				Username: str("notifications.smtp.username"), PasswordSet: str(keySMTPPassword) != "", From: str("notifications.smtp.from")},
			Enabled: boolean("notifications.enabled"), EmailRateLimitPerHour: int(num("notifications.emailRateLimitPerHour")),
		},
	}
}

func (v values) smtp() SMTPConfig {
	str := func(k string) string { s, _ := v[k].(string); return s }
	n, _ := v["notifications.smtp.port"].(int64)
	return SMTPConfig{Host: str("notifications.smtp.host"), Port: int(n), Security: str("notifications.smtp.security"),
		Username: str("notifications.smtp.username"), Password: str(keySMTPPassword), From: str("notifications.smtp.from")}
}

// Snapshot is the resolved state of all settings.
type Snapshot struct {
	Settings Settings          `json:"settings"`
	Sources  map[string]string `json:"sources"`
	Version  int               `json:"version"`
	values   values
}

// Options configure the environment layer and the read-only view.
type Options struct {
	// Env holds values configured by environment variables, keyed by field
	// key (canonical types: string, bool, int64, []string).
	Env map[string]any
	// Readonly is shown as-is by GET /api/admin/settings.
	Readonly map[string]any
	// Box seals the SMTP password in the settings document (nil: the
	// password cannot be stored in the database).
	Box *secretbox.Keyring
	// Production rejects notifications.smtp.security = none.
	Production bool
}

// Service resolves and updates system settings.
type Service struct {
	pool *db.DB
	rec  *audit.Recorder
	log  *slog.Logger
	opts Options

	mu     sync.Mutex
	cached *Snapshot
	at     time.Time
}

// New creates the service and makes sure the settings document exists.
func New(ctx context.Context, pool *db.DB, rec *audit.Recorder, log *slog.Logger, opts Options) (*Service, error) {
	if opts.Readonly == nil {
		opts.Readonly = map[string]any{}
	}
	s := &Service{pool: pool, rec: rec, log: log, opts: opts}
	if err := s.init(ctx); err != nil {
		return nil, fmt.Errorf("init system settings: %w", err)
	}
	return s, nil
}

// init creates the settings document on first start, carrying over the
// pre-Round 5 billing.enforce value.
func (s *Service) init(ctx context.Context) error {
	doc := map[string]any{}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, legacyEnforceKey).Scan(&raw)
	switch {
	case err == nil:
		var legacy struct {
			Enforce bool `json:"enforce"`
		}
		if json.Unmarshal(raw, &legacy) == nil && legacy.Enforce {
			doc["billing.enforce"] = true
		}
	case !db.IsNoRows(err):
		return err
	}
	b, _ := json.Marshal(doc)
	_, err = s.pool.Exec(ctx, `INSERT INTO system_settings (key, value, version, updated_at) VALUES ($1, $2, 1, $3)
		ON CONFLICT (key) DO NOTHING`, rowKey, b, time.Now().UTC())
	return err
}

// resolve computes effective values from a stored document.
func (s *Service) resolve(doc map[string]json.RawMessage, version int) *Snapshot {
	v, src := values{}, map[string]string{}
	for _, f := range fields {
		v[f.key], src[f.key] = f.def, SourceDefault
		if e, ok := s.opts.Env[f.key]; ok {
			v[f.key], src[f.key] = e, SourceEnv
		}
		if raw, ok := doc[f.key]; ok && !isNull(raw) {
			if f.key == keySMTPPassword {
				v[f.key], src[f.key] = s.openPassword(raw), SourceDB
				continue
			}
			val, msg := f.decode(raw)
			if msg != "" {
				s.log.Warn("ignoring invalid stored setting", "key", f.key, "err", msg)
				continue
			}
			v[f.key], src[f.key] = val, SourceDB
		}
	}
	return &Snapshot{Settings: v.settings(), Sources: src, Version: version, values: v}
}

// passwordAD binds the sealed SMTP password to its settings key.
const passwordAD = "system_settings:" + keySMTPPassword

// openPassword decrypts the stored SMTP password ("" = explicitly cleared or
// unreadable, e.g. after the master key changed).
func (s *Service) openPassword(raw json.RawMessage) string {
	var ct string
	if json.Unmarshal(raw, &ct) != nil || ct == "" || s.opts.Box == nil {
		return ""
	}
	pt, err := s.opts.Box.Open(ct, passwordAD)
	if err != nil {
		s.log.Warn("stored SMTP password cannot be decrypted (master key changed?); treating it as unset")
		return ""
	}
	return string(pt)
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func (s *Service) read(ctx context.Context, q db.Querier, lock bool) (map[string]json.RawMessage, int, error) {
	sql := `SELECT value, version FROM system_settings WHERE key = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	var raw []byte
	var version int
	if err := q.QueryRow(ctx, sql, rowKey).Scan(&raw, &version); err != nil {
		return nil, 0, err
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, 0, fmt.Errorf("system settings document: %w", err)
	}
	return doc, version, nil
}

// Get returns the current snapshot (cached for up to CacheTTL).
func (s *Service) Get(ctx context.Context) (*Snapshot, error) {
	s.mu.Lock()
	if s.cached != nil && time.Since(s.at) < CacheTTL {
		snap := s.cached
		s.mu.Unlock()
		return snap, nil
	}
	s.mu.Unlock()
	doc, version, err := s.read(ctx, s.pool, false)
	if err != nil {
		return nil, err
	}
	snap := s.resolve(doc, version)
	s.store(snap)
	return snap, nil
}

func (s *Service) store(snap *Snapshot) {
	s.mu.Lock()
	s.cached, s.at = snap, time.Now()
	s.mu.Unlock()
}

// Current returns the effective settings, falling back to the last known
// values (or env/defaults) when the database is unreachable.
func (s *Service) Current(ctx context.Context) Settings {
	snap, err := s.Get(ctx)
	if err == nil {
		return snap.Settings
	}
	s.log.ErrorContext(ctx, "read system settings", "err", err)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return s.cached.Settings
	}
	return s.resolve(nil, 0).Settings
}

// Actor and RequestMeta identify who changes settings (audit).
type Actor struct {
	ID   uuid.UUID
	Name string
}

type RequestMeta struct {
	IPPrefix  string
	RequestID string
}

// Patch is a partial update by group and field; a JSON null resets the field
// to its environment / default value.
type Patch map[string]map[string]json.RawMessage

// Update applies patch if version matches the current settings version.
func (s *Service) Update(ctx context.Context, a Actor, version int, patch Patch, m RequestMeta) (*Snapshot, error) {
	details := map[string]any{}
	changes := map[string]json.RawMessage{}
	for key, raw := range flatten(patch) {
		f, ok := fieldByKey(key)
		if !ok {
			details[key] = "未知的设置项"
			continue
		}
		if raw == nil || isNull(raw) {
			changes[key] = nil
			continue
		}
		val, msg := f.decode(raw)
		if msg != "" {
			details[key] = msg
			continue
		}
		if key == keySMTPPassword && val.(string) != "" {
			if s.opts.Box == nil {
				details[key] = "未配置主密钥，无法保存密码"
				continue
			}
			ct, err := s.opts.Box.Seal([]byte(val.(string)), passwordAD)
			if err != nil {
				return nil, err
			}
			val = ct
		}
		canon, _ := json.Marshal(val)
		changes[key] = canon
	}
	if len(details) > 0 {
		return nil, apperr.Validation("设置校验失败", details)
	}
	var out *Snapshot
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		doc, cur, err := s.read(ctx, tx, true)
		if err != nil {
			return err
		}
		if cur != version {
			return apperr.VersionConflict()
		}
		before := s.resolve(doc, cur)
		if len(changes) == 0 {
			out = before
			return nil
		}
		for k, raw := range changes {
			if raw == nil {
				delete(doc, k)
			} else {
				doc[k] = raw
			}
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		var next int
		if err := tx.QueryRow(ctx, `UPDATE system_settings SET value = $2, version = version + 1, updated_at = $3, updated_by = $4
			WHERE key = $1 RETURNING version`, rowKey, b, time.Now().UTC(), a.ID).Scan(&next); err != nil {
			return err
		}
		out = s.resolve(doc, next)
		if s.opts.Production && out.values["notifications.smtp.security"] == "none" {
			return apperr.Validation("设置校验失败", map[string]any{"notifications.smtp.security": "生产环境不允许不加密的 SMTP（none）"})
		}
		bv, av := map[string]any{}, map[string]any{}
		var smtpChanged []string
		for k := range changes {
			bv[k], av[k] = before.values[k], out.values[k]
			if k == keySMTPPassword {
				// Never audit the password itself.
				bv[k], av[k] = map[string]bool{"set": before.values[k] != ""}, map[string]bool{"set": out.values[k] != ""}
			}
			if strings.HasPrefix(k, "notifications.smtp.") {
				smtpChanged = append(smtpChanged, strings.TrimPrefix(k, "notifications.smtp."))
			}
		}
		rid := rowKey
		if err := s.rec.Record(ctx, tx, audit.Entry{ActorID: &a.ID, ActorName: &a.Name, Action: ActionUpdate, ResourceType: "system_settings",
			ResourceID: &rid, IPPrefix: m.IPPrefix, RequestID: m.RequestID, Metadata: map[string]any{"before": bv, "after": av}}); err != nil {
			return err
		}
		if len(smtpChanged) == 0 {
			return nil
		}
		slices.Sort(smtpChanged)
		return s.rec.Record(ctx, tx, audit.Entry{ActorID: &a.ID, ActorName: &a.Name, Action: ActionSMTPUpdate, ResourceType: "system_settings",
			ResourceID: &rid, IPPrefix: m.IPPrefix, RequestID: m.RequestID, Metadata: map[string]any{"changed": smtpChanged,
				"host": out.values["notifications.smtp.host"], "security": out.values["notifications.smtp.security"]}})
	})
	if err != nil {
		return nil, err
	}
	s.store(out)
	return out, nil
}

// ---- typed accessors used by other packages ----

// GatewayRetryOn returns gateway.retryOn (retry classes used without a route rule).
func (s *Service) GatewayRetryOn(ctx context.Context) []string {
	return s.Current(ctx).Gateway.RetryOn
}

// GatewayMaxAttempts returns gateway.maxAttempts.
func (s *Service) GatewayMaxAttempts(ctx context.Context) int {
	return s.Current(ctx).Gateway.MaxAttempts
}

// Affinity returns gateway.affinity (the compiled session affinity setting).
func (s *Service) Affinity(ctx context.Context) affinity.Config {
	return s.Current(ctx).Gateway.Affinity
}

// Registration returns the effective registration mode and email domains.
func (s *Service) Registration(ctx context.Context) (string, []string) {
	a := s.Current(ctx).Auth
	return a.RegistrationMode, a.AllowedEmailDomains
}

// SignupCredit returns billing.signupCredit.
func (s *Service) SignupCredit(ctx context.Context) money.Amount {
	a, err := money.Parse(s.Current(ctx).Billing.SignupCredit)
	if err != nil {
		return 0
	}
	return a
}

// PublicModelPlaza returns site.publicModelPlaza.
func (s *Service) PublicModelPlaza(ctx context.Context) bool {
	return s.Current(ctx).Site.PublicModelPlaza
}

// LogRetentionDays returns gateway.logRetentionDays (0 = keep forever).
func (s *Service) LogRetentionDays(ctx context.Context) int {
	return s.Current(ctx).Gateway.LogRetentionDays
}

// BillingEnforced implements billing's enforcement source.
func (s *Service) BillingEnforced(ctx context.Context) (bool, error) {
	snap, err := s.Get(ctx)
	if err != nil {
		return false, err
	}
	return snap.Settings.Billing.Enforce, nil
}

// BillingEnforce returns billing.enforce and the settings version (for the
// /api/admin/billing/settings alias).
func (s *Service) BillingEnforce(ctx context.Context) (bool, int, error) {
	snap, err := s.Get(ctx)
	if err != nil {
		return false, 0, err
	}
	return snap.Settings.Billing.Enforce, snap.Version, nil
}

// SetBillingEnforce updates billing.enforce through the settings document.
func (s *Service) SetBillingEnforce(ctx context.Context, actorID uuid.UUID, actorName string, enforce bool, version int, ipPrefix, requestID string) (bool, int, error) {
	raw, _ := json.Marshal(enforce)
	snap, err := s.Update(ctx, Actor{ID: actorID, Name: actorName}, version, Patch{"billing": {"enforce": raw}},
		RequestMeta{IPPrefix: ipPrefix, RequestID: requestID})
	if err != nil {
		return false, 0, err
	}
	return snap.Settings.Billing.Enforce, snap.Version, nil
}

// flatten turns the nested PATCH document into flat field keys: groups map
// fields to values, and an object under a key that is not a field itself is
// one more level ({"notifications": {"smtp": {"host": …}}} →
// "notifications.smtp.host").
func flatten(p Patch) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for group, fieldsIn := range p {
		for name, raw := range fieldsIn {
			key := group + "." + name
			if _, ok := fieldByKey(key); !ok {
				var nested map[string]json.RawMessage
				if json.Unmarshal(raw, &nested) == nil && nested != nil {
					for sub, v := range nested {
						out[key+"."+sub] = v
					}
					continue
				}
			}
			out[key] = raw
		}
	}
	return out
}

// SMTP returns the effective SMTP configuration (with the password).
func (s *Service) SMTP(ctx context.Context) SMTPConfig {
	snap, err := s.Get(ctx)
	if err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cached != nil {
			return s.cached.values.smtp()
		}
		return s.resolve(nil, 0).values.smtp()
	}
	return snap.values.smtp()
}

// NotificationsEnabled returns notifications.enabled.
func (s *Service) NotificationsEnabled(ctx context.Context) bool {
	return s.Current(ctx).Notifications.Enabled
}

// EmailRateLimitPerHour returns notifications.emailRateLimitPerHour.
func (s *Service) EmailRateLimitPerHour(ctx context.Context) int {
	return s.Current(ctx).Notifications.EmailRateLimitPerHour
}

// SiteName returns site.name.
func (s *Service) SiteName(ctx context.Context) string { return s.Current(ctx).Site.Name }

// Readonly returns the environment-only configuration shown by the settings page.
func (s *Service) Readonly() map[string]any { return s.opts.Readonly }

// decodeRate accepts a percentage 0–100 with at most 2 decimals (a decimal
// string, or a JSON number for convenience).
func decodeRate(raw json.RawMessage) (any, string) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var n json.Number
		if json.Unmarshal(raw, &n) != nil {
			return nil, "必须是十进制字符串"
		}
		s = n.String()
	}
	a, err := money.Parse(strings.TrimSpace(s))
	if err != nil || a < 0 || a > money.MustParse("100") || a%money.MustParse("0.01") != 0 {
		return nil, "必须是 0–100 的数，最多 2 位小数"
	}
	return a.String(), ""
}

// Referral is the effective referral rebate configuration.
type Referral struct {
	Enabled bool
	// Rate is the percentage string; RateBP is it in basis points.
	Rate        string
	RateBP      int64
	MinRecharge money.Amount
}

// Referral returns billing.referral*.
func (s *Service) Referral(ctx context.Context) Referral {
	b := s.Current(ctx).Billing
	out := Referral{Enabled: b.ReferralEnabled, Rate: b.ReferralRate}
	if a, err := money.Parse(b.ReferralRate); err == nil {
		out.RateBP = int64(a / money.MustParse("0.01"))
	}
	if a, err := money.Parse(b.ReferralMinRecharge); err == nil {
		out.MinRecharge = a
	}
	return out
}
