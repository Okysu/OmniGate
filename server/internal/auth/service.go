package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/config"
	"omnigate/internal/identity"
)

// Login denial codes are shown on the login page via /login?error=<code>.
const (
	DenyRegistrationClosed = "registration_closed"
	DenyNotAllowed         = "not_allowed"
	DenyAccountDisabled    = "account_disabled"
)

// Service implements login admission, account provisioning and sessions.
type Service struct {
	cfg      config.AuthConfig
	dev      bool
	loopback bool // public URL host is localhost / loopback
	store    *identity.Store
	audit    *audit.Recorder
	log      *slog.Logger
	now      func() time.Time

	// Registration, when set, returns the effective registration mode and
	// allowed email domains (system settings) instead of the static config.
	Registration func(ctx context.Context) (mode string, emailDomains []string)
	// OnUserCreated runs after an account is provisioned on first login (for
	// example to credit a sign-up bonus). Errors are logged, never fatal.
	OnUserCreated func(ctx context.Context, user *identity.User, meta RequestMeta) error
}

func NewService(cfg config.AuthConfig, dev, loopback bool, store *identity.Store, rec *audit.Recorder, log *slog.Logger) *Service {
	return &Service{cfg: cfg, dev: dev, loopback: loopback, store: store, audit: rec, log: log, now: func() time.Time { return time.Now().UTC() }}
}

// IsLoopbackHost reports whether host (without port) is localhost or a loopback IP.
func IsLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.IsLoopback()
}

// RequestMeta carries client information for sessions and audit entries.
type RequestMeta struct {
	UserAgent string
	IPPrefix  string
	RequestID string
	// Invite is the invite code carried through the OAuth round trip
	// (phase15-api.md §4.2); only used when the login creates the account.
	Invite string
}

// LoginResult is returned by CompleteLogin.
type LoginResult struct {
	User      *identity.User
	Token     string
	ExpiresAt time.Time
	Created   bool
}

// CompleteLogin admits (or denies) an external identity, provisions the account
// on first login and issues a session token.
func (s *Service) CompleteLogin(ctx context.Context, ext identity.External, meta RequestMeta) (*LoginResult, error) {
	now := s.now()
	user, err := s.store.FindByIdentity(ctx, ext.Provider, ext.Subject)
	if err != nil {
		return nil, err
	}
	created := false
	if user == nil {
		role, deny, err := s.admit(ctx, ext)
		if err != nil {
			return nil, err
		}
		if deny != "" {
			s.recordDenied(ctx, ext, deny, meta)
			return nil, apperr.New(apperr.KindForbidden, deny, "登录被拒绝")
		}
		user, err = s.store.CreateWithIdentity(ctx, ext, role, now)
		if err != nil {
			return nil, err
		}
		created = true
		_ = s.audit.Record(ctx, nil, audit.Entry{
			ActorID: &user.ID, ActorName: &user.DisplayName, Action: audit.ActionUserCreate,
			ResourceType: "user", ResourceID: ptr(user.ID.String()), IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
			Metadata: map[string]any{"provider": ext.Provider, "login": ext.Login, "role": role},
		})
		if s.OnUserCreated != nil {
			if err := s.OnUserCreated(ctx, user, meta); err != nil {
				s.log.ErrorContext(ctx, "post-provisioning hook failed", "user_id", user.ID, "err", err)
			}
		}
	} else {
		if !user.ActiveAt(now) {
			s.recordDenied(ctx, ext, DenyAccountDisabled, meta)
			// The login page shows the reason and end of the suspension
			// (phase7-api.md §2.1, §4.2).
			e := apperr.New(apperr.KindForbidden, DenyAccountDisabled, "账号已停用")
			e.Details = map[string]any{}
			if user.DisabledReason != nil && *user.DisabledReason != "" {
				e.Details["reason"] = *user.DisabledReason
			}
			if user.DisabledUntil != nil {
				e.Details["until"] = user.DisabledUntil.UTC().Format(time.RFC3339)
			}
			return nil, e
		}
		if err := s.store.RecordLogin(ctx, user.ID, ext, now); err != nil {
			return nil, err
		}
	}

	token, err := newToken()
	if err != nil {
		return nil, err
	}
	expires := now.Add(s.cfg.SessionTTL)
	sid, err := s.store.CreateSession(ctx, user.ID, token, meta.UserAgent, meta.IPPrefix, now, expires)
	if err != nil {
		return nil, err
	}
	_ = s.audit.Record(ctx, nil, audit.Entry{
		ActorID: &user.ID, ActorName: &user.DisplayName, Action: audit.ActionLogin,
		ResourceType: "session", ResourceID: ptr(sid.String()), IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
		Metadata: map[string]any{"provider": ext.Provider, "login": ext.Login},
	})
	return &LoginResult{User: user, Token: token, ExpiresAt: expires, Created: created}, nil
}

// admit decides whether a new identity may create an account and with which role.
func (s *Service) admit(ctx context.Context, ext identity.External) (identity.Role, string, error) {
	if matchAny(s.cfg.BootstrapAdmins, ext) {
		return identity.RoleSystemAdmin, "", nil
	}
	// Development convenience: with no bootstrap admins configured, the very first
	// account becomes system admin — but only when the public URL is a loopback
	// address, so a dev-mode instance accidentally exposed on a real hostname can't
	// be claimed by a stranger. Production refuses to start without bootstrap admins.
	if s.dev && s.loopback && len(s.cfg.BootstrapAdmins) == 0 {
		n, err := s.store.CountUsers(ctx)
		if err != nil {
			return "", "", err
		}
		if n == 0 {
			s.log.Warn("development mode: first user becomes system_admin", "provider", ext.Provider, "login", ext.Login)
			return identity.RoleSystemAdmin, "", nil
		}
	}
	mode, domains := s.cfg.RegistrationMode, s.cfg.AllowedEmailDomains
	if s.Registration != nil {
		m, d := s.Registration(ctx)
		mode, domains = config.RegistrationMode(m), d
	}
	switch mode {
	case config.RegistrationOpen:
		return identity.RoleUser, "", nil
	case config.RegistrationRestricted:
		if matchAny(s.cfg.AllowedIdentities, ext) || emailDomainAllowed(ext, domains) {
			return identity.RoleUser, "", nil
		}
		return "", DenyNotAllowed, nil
	default:
		return "", DenyRegistrationClosed, nil
	}
}

func emailDomainAllowed(ext identity.External, domains []string) bool {
	if !ext.EmailVerified || ext.Email == "" {
		return false
	}
	_, domain, ok := strings.Cut(ext.Email, "@")
	if !ok {
		return false
	}
	for _, d := range domains {
		if strings.EqualFold(domain, d) {
			return true
		}
	}
	return false
}

// matchAny implements identity matchers:
//
//	github:<login>          GitHub login (case-insensitive; logins can be renamed)
//	github-id:<numeric id>  GitHub immutable user id (recommended)
//	<oidc-id>:<sub>         OIDC subject
//	email:<address>         any provider, verified email only
func matchAny(ms []config.IdentityMatcher, ext identity.External) bool {
	for _, m := range ms {
		switch {
		case m.Provider == "email":
			if ext.EmailVerified && strings.EqualFold(m.Value, ext.Email) {
				return true
			}
		case m.Provider == "github" && ext.Provider == "github":
			if strings.EqualFold(m.Value, ext.Login) {
				return true
			}
		case m.Provider == ext.Provider+"-id" || (m.Provider == ext.Provider && ext.Provider != "github"):
			if m.Value == ext.Subject {
				return true
			}
		}
	}
	return false
}

func (s *Service) recordDenied(ctx context.Context, ext identity.External, reason string, meta RequestMeta) {
	_ = s.audit.Record(ctx, nil, audit.Entry{
		Action: audit.ActionLoginDenied, ResourceType: "identity",
		ResourceID: ptr(ext.Provider + ":" + ext.Subject), IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
		Metadata: map[string]any{"reason": reason, "login": ext.Login},
	})
}

// Authenticate resolves a session cookie to a principal. Disabled users and
// expired/idle sessions yield (nil, nil).
func (s *Service) Authenticate(ctx context.Context, token string) (*authz.Principal, *identity.User, error) {
	if token == "" {
		return nil, nil, nil
	}
	now := s.now()
	su, err := s.store.LookupSession(ctx, token, now, s.cfg.SessionIdleTTL)
	if err != nil || su == nil {
		return nil, nil, err
	}
	if !su.User.ActiveAt(now) {
		return nil, nil, nil
	}
	// Throttle last_seen writes to once per minute per session.
	if now.Sub(su.LastSeenAt) > time.Minute {
		if err := s.store.TouchSession(ctx, su.SessionID, now); err != nil {
			s.log.WarnContext(ctx, "touch session failed", "err", err)
		}
	}
	return &authz.Principal{UserID: su.User.ID, Name: su.User.DisplayName, Role: su.User.Role, SessionID: su.SessionID}, su.User, nil
}

func (s *Service) Logout(ctx context.Context, p *authz.Principal, meta RequestMeta) error {
	if err := s.store.RevokeSession(ctx, p.UserID, p.SessionID, "logout", s.now()); err != nil {
		return err
	}
	return s.audit.Record(ctx, nil, audit.Entry{
		ActorID: &p.UserID, ActorName: &p.Name, Action: audit.ActionLogout, ResourceType: "session",
		ResourceID: ptr(p.SessionID.String()), IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
	})
}

func (s *Service) ListSessions(ctx context.Context, p *authz.Principal) ([]identity.Session, error) {
	list, err := s.store.ListSessions(ctx, p.UserID, s.now(), s.cfg.SessionIdleTTL)
	for i := range list {
		list[i].Current = list[i].ID == p.SessionID
	}
	return list, err
}

func (s *Service) RevokeSession(ctx context.Context, p *authz.Principal, id uuid.UUID, meta RequestMeta) error {
	if err := s.store.RevokeSession(ctx, p.UserID, id, "user_revoked", s.now()); err != nil {
		return err
	}
	return s.audit.Record(ctx, nil, audit.Entry{
		ActorID: &p.UserID, ActorName: &p.Name, Action: audit.ActionSessionRevoke, ResourceType: "session",
		ResourceID: ptr(id.String()), IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
	})
}

func (s *Service) RevokeOtherSessions(ctx context.Context, p *authz.Principal, meta RequestMeta) (int64, error) {
	n, err := s.store.RevokeOtherSessions(ctx, p.UserID, p.SessionID, "user_revoked_others", s.now())
	if err != nil {
		return 0, err
	}
	return n, s.audit.Record(ctx, nil, audit.Entry{
		ActorID: &p.UserID, ActorName: &p.Name, Action: audit.ActionSessionRevokeOther, ResourceType: "session",
		IPPrefix: meta.IPPrefix, RequestID: meta.RequestID, Metadata: map[string]any{"revoked": n},
	})
}

// newToken returns 256 bits of randomness, base64url encoded.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func ptr[T any](v T) *T { return &v }
