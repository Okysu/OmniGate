// Package keys manages Gateway Keys used by SDK clients to call /v1/*.
// Keys are 256-bit random secrets shown once; only their SHA-256 digest and a
// display prefix are stored (ADR-0008).
package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

const Prefix = "og-"

type Policy struct {
	AllowedModels   []string    `json:"allowedModels"`
	AllowedChannels []uuid.UUID `json:"allowedChannels"`
	IPAllowlist     []string    `json:"ipAllowlist"`
	RPM             *int        `json:"rpm"`
	CompatMode      string      `json:"compatMode"` // strict | lenient
	// QuotaOverflow overrides the owner's billing preference for this key when
	// subscriptions are out of quota: "" (follow the user), "block" or "wallet".
	QuotaOverflow string `json:"quotaOverflow"`
	// SpendLimit caps the key's platform-channel charges per window (nil =
	// none; phase8-api.md §2.1).
	SpendLimit *SpendLimit `json:"spendLimit"`
}

// SpendLimit is a key's spend cap. Windows follow the owner's group timezone
// (weeks start on Monday).
type SpendLimit struct {
	Amount string `json:"amount"`
	Window string `json:"window"` // day | week | month | total
}

// Spend limit windows.
const (
	WindowDay   = "day"
	WindowWeek  = "week"
	WindowMonth = "month"
	WindowTotal = "total"
)

// Cap returns the parsed amount (ok=false when the limit is unusable).
func (l *SpendLimit) Cap() (money.Amount, bool) {
	if l == nil {
		return 0, false
	}
	a, err := money.Parse(l.Amount)
	return a, err == nil && a >= 0
}

type Key struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Status     string     `json:"status"`
	Policy     Policy     `json:"policy"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	Version    int        `json:"version"`
	UserID     uuid.UUID  `json:"-"`
}

// Input for create / update.
type Input struct {
	Name      *string    `json:"name"`
	Status    *string    `json:"status"`
	Policy    *Policy    `json:"policy"`
	ExpiresAt *time.Time `json:"expiresAt"`
	// ClearExpiry removes the expiry on update (JSON null can't be told apart from absent).
	ClearExpiry bool `json:"clearExpiry"`
	Version     *int `json:"version"`
}

func normalizePolicy(p *Policy) error {
	details := map[string]any{}
	if p.CompatMode == "" {
		p.CompatMode = "strict"
	}
	if p.CompatMode != "strict" && p.CompatMode != "lenient" {
		details["policy.compatMode"] = "只能是 strict 或 lenient"
	}
	if p.QuotaOverflow != "" && p.QuotaOverflow != "block" && p.QuotaOverflow != "wallet" {
		details["policy.quotaOverflow"] = "只能为空（跟随账户设置）、block 或 wallet"
	}
	if p.AllowedModels == nil {
		p.AllowedModels = []string{}
	}
	if p.AllowedChannels == nil {
		p.AllowedChannels = []uuid.UUID{}
	}
	if p.IPAllowlist == nil {
		p.IPAllowlist = []string{}
	}
	if len(p.AllowedModels) > 200 || len(p.AllowedChannels) > 200 || len(p.IPAllowlist) > 100 {
		details["policy"] = "列表项过多"
	}
	for i, c := range p.IPAllowlist {
		pr, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			a, err2 := netip.ParseAddr(strings.TrimSpace(c))
			if err2 != nil {
				details["policy.ipAllowlist"] = "无效的 IP 或 CIDR：" + c
				continue
			}
			pr = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		p.IPAllowlist[i] = pr.Masked().String()
	}
	if p.RPM != nil && (*p.RPM < 1 || *p.RPM > 100000) {
		details["policy.rpm"] = "范围为 1–100000，留空表示不限"
	}
	if l := p.SpendLimit; l != nil {
		a, err := money.Parse(strings.TrimSpace(l.Amount))
		if err != nil || a < 0 {
			details["policy.spendLimit.amount"] = "必须是不小于 0 的十进制金额（最多 9 位小数）"
		} else {
			l.Amount = a.String()
		}
		switch l.Window {
		case WindowDay, WindowWeek, WindowMonth, WindowTotal:
		default:
			details["policy.spendLimit.window"] = "只能是 day、week、month 或 total"
		}
	}
	if len(details) > 0 {
		return apperr.Validation("Key 策略校验失败", details)
	}
	return nil
}

// AllowsIP reports whether ip passes the allowlist (empty = any).
func (p *Policy) AllowsIP(ip netip.Addr) bool {
	if len(p.IPAllowlist) == 0 {
		return true
	}
	ip = ip.Unmap()
	for _, c := range p.IPAllowlist {
		if pr, err := netip.ParsePrefix(c); err == nil && pr.Contains(ip) {
			return true
		}
	}
	return false
}

// AllowsModel reports whether model passes the allowlist (empty = any).
func (p *Policy) AllowsModel(model string) bool {
	if len(p.AllowedModels) == 0 {
		return true
	}
	for _, m := range p.AllowedModels {
		if m == model {
			return true
		}
	}
	return false
}

func hashKey(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Service manages keys and authenticates gateway requests.
type Service struct {
	pool  *db.DB
	audit *audit.Recorder
	now   func() time.Time

	mu       sync.Mutex
	cache    map[string]cacheEntry // hex(hash) -> entry
	limiters map[uuid.UUID]*rate.Limiter
	touched  map[uuid.UUID]time.Time
}

type cacheEntry struct {
	auth    *Auth
	expires time.Time
}

func NewService(pool *db.DB, rec *audit.Recorder) *Service {
	return &Service{pool: pool, audit: rec, now: func() time.Time { return time.Now().UTC() },
		cache: map[string]cacheEntry{}, limiters: map[uuid.UUID]*rate.Limiter{}, touched: map[uuid.UUID]time.Time{}}
}

const keyCols = `id, user_id, name, prefix, status, policy, expires_at, last_used_at, created_at, version`

func scanKey(row db.Row) (*Key, error) {
	var k Key
	var pol []byte
	if err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.Status, &pol, &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.Version); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(pol, &k.Policy)
	_ = normalizePolicy(&k.Policy)
	return &k, nil
}

func (s *Service) List(ctx context.Context, p *authz.Principal) ([]*Key, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+keyCols+` FROM gateway_keys WHERE user_id = $1 AND status <> 'revoked' ORDER BY created_at DESC`, p.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Key{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Created is returned once on create/rotate.
type Created struct {
	Key    *Key   `json:"key"`
	Secret string `json:"secret"`
}

type Meta struct {
	IPPrefix  string
	RequestID string
}

func (s *Service) record(ctx context.Context, q db.Querier, p *authz.Principal, meta Meta, action string, k *Key, md map[string]any) error {
	id := k.ID.String()
	if md == nil {
		md = map[string]any{}
	}
	md["name"], md["prefix"] = k.Name, k.Prefix
	return s.audit.Record(ctx, q, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: action, ResourceType: "gateway_key",
		ResourceID: &id, IPPrefix: meta.IPPrefix, RequestID: meta.RequestID, Metadata: md})
}

func validateName(name string) error {
	if n := len([]rune(strings.TrimSpace(name))); n == 0 || n > 64 {
		return apperr.Validation("参数校验失败", map[string]any{"name": "长度应为 1–64 个字符"})
	}
	return nil
}

func (s *Service) insert(ctx context.Context, tx db.Tx, userID uuid.UUID, name string, pol Policy, expires *time.Time, rotatedFrom *uuid.UUID) (*Created, error) {
	secret, err := newSecret()
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	polJSON, _ := json.Marshal(pol)
	k, err := scanKey(tx.QueryRow(ctx, `INSERT INTO gateway_keys (id, user_id, name, prefix, key_hash, policy, expires_at, rotated_from, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+keyCols,
		id, userID, strings.TrimSpace(name), secret[:12]+"…", hashKey(secret), polJSON, expires, rotatedFrom, s.now()))
	if err != nil {
		return nil, err
	}
	return &Created{Key: k, Secret: secret}, nil
}

func (s *Service) Create(ctx context.Context, p *authz.Principal, in Input, meta Meta) (*Created, error) {
	if !p.Can(authz.KeysOwn) {
		return nil, apperr.Forbidden()
	}
	if in.Name == nil {
		return nil, apperr.Validation("参数校验失败", map[string]any{"name": "必填"})
	}
	if err := validateName(*in.Name); err != nil {
		return nil, err
	}
	pol := Policy{}
	if in.Policy != nil {
		pol = *in.Policy
	}
	if err := normalizePolicy(&pol); err != nil {
		return nil, err
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.now()) {
		return nil, apperr.Validation("参数校验失败", map[string]any{"expiresAt": "必须晚于当前时间"})
	}
	var out *Created
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var err error
		if out, err = s.insert(ctx, tx, p.UserID, *in.Name, pol, in.ExpiresAt, nil); err != nil {
			return err
		}
		return s.record(ctx, tx, p, meta, "key.create", out.Key, nil)
	})
	return out, err
}

func (s *Service) ownKey(ctx context.Context, q db.Querier, p *authz.Principal, id uuid.UUID, lock bool) (*Key, error) {
	sql := `SELECT ` + keyCols + ` FROM gateway_keys WHERE id = $1 AND user_id = $2 AND status <> 'revoked'`
	if lock {
		sql += ` FOR UPDATE`
	}
	k, err := scanKey(q.QueryRow(ctx, sql, id, p.UserID))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("API Key")
	}
	return k, err
}

func (s *Service) Update(ctx context.Context, p *authz.Principal, id uuid.UUID, in Input, meta Meta) (*Key, error) {
	if in.Version == nil {
		return nil, apperr.Validation("参数校验失败", map[string]any{"version": "必填"})
	}
	var out *Key
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		k, err := s.ownKey(ctx, tx, p, id, true)
		if err != nil {
			return err
		}
		if k.Version != *in.Version {
			return apperr.VersionConflict()
		}
		if in.Name != nil {
			if err := validateName(*in.Name); err != nil {
				return err
			}
			k.Name = strings.TrimSpace(*in.Name)
		}
		if in.Status != nil {
			if *in.Status != "enabled" && *in.Status != "disabled" {
				return apperr.Validation("参数校验失败", map[string]any{"status": "只能是 enabled 或 disabled"})
			}
			k.Status = *in.Status
		}
		if in.Policy != nil {
			pol := *in.Policy
			if err := normalizePolicy(&pol); err != nil {
				return err
			}
			k.Policy = pol
		}
		if in.ClearExpiry {
			k.ExpiresAt = nil
		} else if in.ExpiresAt != nil {
			if !in.ExpiresAt.After(s.now()) {
				return apperr.Validation("参数校验失败", map[string]any{"expiresAt": "必须晚于当前时间"})
			}
			k.ExpiresAt = in.ExpiresAt
		}
		polJSON, _ := json.Marshal(k.Policy)
		out, err = scanKey(tx.QueryRow(ctx, `UPDATE gateway_keys SET name = $2, status = $3, policy = $4, expires_at = $5, version = version + 1
			WHERE id = $1 RETURNING `+keyCols, id, k.Name, k.Status, polJSON, k.ExpiresAt))
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, meta, "key.update", out, nil)
	})
	if err == nil {
		s.InvalidateCache()
	}
	return out, err
}

func (s *Service) Revoke(ctx context.Context, p *authz.Principal, id uuid.UUID, meta Meta) error {
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		k, err := s.ownKey(ctx, tx, p, id, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE gateway_keys SET status = 'revoked', revoked_at = $2, version = version + 1 WHERE id = $1`, id, s.now()); err != nil {
			return err
		}
		return s.record(ctx, tx, p, meta, "key.revoke", k, nil)
	})
	if err == nil {
		s.InvalidateCache()
	}
	return err
}

func (s *Service) Rotate(ctx context.Context, p *authz.Principal, id uuid.UUID, meta Meta) (*Created, error) {
	var out *Created
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		old, err := s.ownKey(ctx, tx, p, id, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE gateway_keys SET status = 'revoked', revoked_at = $2, version = version + 1 WHERE id = $1`, id, s.now()); err != nil {
			return err
		}
		if out, err = s.insert(ctx, tx, p.UserID, old.Name, old.Policy, old.ExpiresAt, &old.ID); err != nil {
			return err
		}
		if old.Status == "disabled" {
			if _, err := tx.Exec(ctx, `UPDATE gateway_keys SET status = 'disabled' WHERE id = $1`, out.Key.ID); err != nil {
				return err
			}
			out.Key.Status = "disabled"
		}
		return s.record(ctx, tx, p, meta, "key.rotate", out.Key, map[string]any{"rotatedFrom": old.ID.String()})
	})
	if err == nil {
		s.InvalidateCache()
	}
	return out, err
}

// ---- data plane authentication ----

// Auth is the result of authenticating a gateway key.
type Auth struct {
	Key      *Key
	UserID   uuid.UUID
	UserName string
	UserRole identity.Role
	// GroupID is the owner's user group (nil only for rows written without
	// one; the default group applies).
	GroupID *uuid.UUID
	// UserDisabled is set when the key is valid but its owner is disabled:
	// the gateway answers 403 account_disabled (phase7-api.md §2.1).
	UserDisabled *Disabled
}

// Disabled describes a suspended key owner.
type Disabled struct {
	Reason string
	Until  *time.Time // nil = permanent
}

const cacheTTL = 10 * time.Second

// InvalidateCache drops cached authentications (after key or user changes).
func (s *Service) InvalidateCache() {
	s.mu.Lock()
	s.cache = map[string]cacheEntry{}
	s.mu.Unlock()
}

// Authenticate resolves a presented secret. It returns (nil, nil) for unknown,
// revoked, disabled or expired keys. A valid key of a disabled user yields an
// Auth with UserDisabled set (a suspension whose end has passed counts as
// enabled already, before the auto-enable job runs). Results are cached for
// 10s; key and user changes through the services clear the cache instantly.
func (s *Service) Authenticate(ctx context.Context, secret string) (*Auth, error) {
	if !strings.HasPrefix(secret, Prefix) || len(secret) > 128 {
		return nil, nil
	}
	h := hashKey(secret)
	ck := string(h)
	now := s.now()
	s.mu.Lock()
	if e, ok := s.cache[ck]; ok && now.Before(e.expires) {
		s.mu.Unlock()
		return s.valid(e.auth, now), nil
	}
	s.mu.Unlock()

	var a Auth
	var userStatus string
	var reason *string
	var until *time.Time
	k, err := scanKeyWithUser(s.pool.QueryRow(ctx, `SELECT k.id, k.user_id, k.name, k.prefix, k.status, k.policy, k.expires_at,
		k.last_used_at, k.created_at, k.version, u.display_name, u.status, u.role, u.disabled_reason, u.disabled_until, u.group_id
		FROM gateway_keys k JOIN users u ON u.id = k.user_id WHERE k.key_hash = $1`, h), &a.UserName, &userStatus, &a.UserRole,
		&reason, &until, &a.GroupID)
	if db.IsNoRows(err) {
		s.put(ck, nil, now)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if userStatus != string(identity.StatusActive) && (until == nil || until.After(now)) {
		d := &Disabled{Until: until}
		if reason != nil {
			d.Reason = *reason
		}
		a.UserDisabled = d
	}
	a.Key, a.UserID = k, k.UserID
	s.put(ck, &a, now)
	return s.valid(&a, now), nil
}

func (s *Service) valid(a *Auth, now time.Time) *Auth {
	if a == nil || a.Key.Status != "enabled" || (a.Key.ExpiresAt != nil && !now.Before(*a.Key.ExpiresAt)) {
		return nil
	}
	return a
}

func (s *Service) put(k string, a *Auth, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) > 10000 {
		s.cache = map[string]cacheEntry{}
	}
	s.cache[k] = cacheEntry{auth: a, expires: now.Add(cacheTTL)}
}

func scanKeyWithUser(row db.Row, name, status *string, role *identity.Role, extra ...any) (*Key, error) {
	var k Key
	var pol []byte
	dst := append([]any{&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.Status, &pol, &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.Version, name, status, role}, extra...)
	if err := row.Scan(dst...); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(pol, &k.Policy)
	_ = normalizePolicy(&k.Policy)
	return &k, nil
}

// AllowRequest applies the key's RPM limit (in-process).
func (s *Service) AllowRequest(k *Key) bool {
	if k.Policy.RPM == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.limiters[k.ID]
	rpm := *k.Policy.RPM
	if !ok || l.Burst() != rpm {
		l = rate.NewLimiter(rate.Limit(float64(rpm)/60), rpm)
		s.limiters[k.ID] = l
	}
	return l.Allow()
}

// Touch updates last_used_at at most once per minute per key.
func (s *Service) Touch(ctx context.Context, id uuid.UUID) {
	now := s.now()
	s.mu.Lock()
	if t, ok := s.touched[id]; ok && now.Sub(t) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.touched[id] = now
	s.mu.Unlock()
	_, _ = s.pool.Exec(ctx, `UPDATE gateway_keys SET last_used_at = $2 WHERE id = $1`, id, now)
}

// DisableAllForUser disables every enabled key of userID (admin action,
// phase7-api.md §2.3) and returns how many were disabled. The keys can be
// enabled again individually.
func (s *Service) DisableAllForUser(ctx context.Context, q db.Querier, userID uuid.UUID) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE gateway_keys SET status = 'disabled', version = version + 1
		WHERE user_id = $1 AND status = 'enabled'`, userID)
	if err != nil {
		return 0, err
	}
	s.InvalidateCache()
	return tag.RowsAffected(), nil
}

// Summary is the admin view of a user's key (no policy).
type Summary struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Status     string     `json:"status"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// ListForUser returns userID's keys (not revoked), newest first.
func (s *Service) ListForUser(ctx context.Context, userID uuid.UUID) ([]Summary, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, prefix, status, last_used_at, created_at FROM gateway_keys
		WHERE user_id = $1 AND status <> 'revoked' ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var k Summary
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Status, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
