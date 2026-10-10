package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"

	"omnigate/internal/apperr"
	"omnigate/internal/authz"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/platform/secretbox"
)

const (
	SessionCookie = "og_session"
	stateCookie   = "og_oauth"
	stateTTL      = 10 * time.Minute
)

type principalKey struct{}

// PrincipalFrom returns the authenticated principal, or nil.
func PrincipalFrom(ctx context.Context) *authz.Principal {
	p, _ := ctx.Value(principalKey{}).(*authz.Principal)
	return p
}

// WithPrincipal stores p in ctx (used by tests and internal callers).
func WithPrincipal(ctx context.Context, p *authz.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// Handler serves /api/auth/* and /api/me/*.
type Handler struct {
	svc         *Service
	providers   map[string]Provider
	order       []Provider
	stateBox    *secretbox.Keyring
	publicURL   *url.URL
	secure      bool
	log         *slog.Logger
	loginLimits *limiter

	// MeGroup returns the user's own group for /api/me (phase8-api.md §1.3);
	// nil omits it.
	MeGroup func(ctx context.Context, userID uuid.UUID) (any, error)
}

func NewHandler(svc *Service, providers []Provider, stateBox *secretbox.Keyring, publicURL *url.URL, secure bool, log *slog.Logger) *Handler {
	h := &Handler{
		svc: svc, providers: map[string]Provider{}, order: providers, stateBox: stateBox,
		publicURL: publicURL, secure: secure, log: log,
		loginLimits: newLimiter(rate.Every(2*time.Second), 30),
	}
	for _, p := range providers {
		h.providers[p.ID()] = p
	}
	return h
}

// Routes mounts public auth endpoints (no session required).
func (h *Handler) Routes(r chi.Router) {
	r.Get("/auth/providers", h.listProviders)
	r.Get("/auth/{provider}/login", h.login)
	r.Get("/auth/{provider}/callback", h.callback)
}

// SessionRoutes mounts endpoints that require an authenticated session.
func (h *Handler) SessionRoutes(r chi.Router) {
	r.Post("/auth/logout", h.logout)
	r.Get("/me", h.me)
	r.Get("/me/sessions", h.listSessions)
	r.Delete("/me/sessions/{id}", h.revokeSession)
	r.Post("/me/sessions/revoke-others", h.revokeOthers)
}

func (h *Handler) listProviders(w http.ResponseWriter, _ *http.Request) {
	type item struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		DisplayName string `json:"displayName"`
	}
	out := []item{}
	for _, p := range h.order {
		out = append(out, item{p.ID(), p.Type(), p.DisplayName()})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"providers": out})
}

type oauthState struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Redirect string `json:"r"`
	Invite   string `json:"i,omitempty"`
	Expires  int64  `json:"e"`
}

func (h *Handler) callbackURL(provider string) string {
	return h.publicURL.String() + "/api/auth/" + url.PathEscape(provider) + "/callback"
}

// login and callback are browser navigations, so failures redirect back to the
// login page with an error code instead of returning JSON.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	p, ok := h.providers[chi.URLParam(r, "provider")]
	if !ok {
		h.fail(w, r, "unknown_provider")
		return
	}
	if !h.loginLimits.allow(httpx.IPPrefix(httpx.ClientIP(r.Context()))) {
		h.fail(w, r, "rate_limited")
		return
	}
	st := oauthState{
		Provider: p.ID(), State: randString(), Nonce: randString(), Verifier: oauth2.GenerateVerifier(),
		Redirect: SafeRedirect(r.URL.Query().Get("redirect")), Expires: time.Now().Add(stateTTL).Unix(),
	}
	if inv := strings.TrimSpace(r.URL.Query().Get("invite")); len(inv) <= 32 {
		st.Invite = inv
	}
	raw, _ := json.Marshal(st)
	sealed, err := h.stateBox.Seal(raw, stateCookie)
	if err != nil {
		httpx.WriteError(w, r, apperr.Internal(err))
		return
	}
	target, err := p.AuthCodeURL(r.Context(), st.State, st.Nonce, st.Verifier, h.callbackURL(p.ID()))
	if err != nil {
		h.log.ErrorContext(r.Context(), "build auth url", "provider", p.ID(), "err", err)
		h.fail(w, r, "oauth_failed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: sealed, Path: "/api/auth/", MaxAge: int(stateTTL.Seconds()),
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	providerID := chi.URLParam(r, "provider")
	p, ok := h.providers[providerID]
	if !ok {
		h.fail(w, r, "unknown_provider")
		return
	}
	if !h.loginLimits.allow(httpx.IPPrefix(httpx.ClientIP(r.Context()))) {
		h.fail(w, r, "rate_limited")
		return
	}
	// The state cookie is single-use: clear it whatever happens next.
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/api/auth/", MaxAge: -1,
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode})

	st, err := h.readState(r)
	if err != nil || st.Provider != providerID || st.State == "" || r.URL.Query().Get("state") != st.State {
		h.fail(w, r, "state_mismatch")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		h.log.InfoContext(r.Context(), "idp returned error", "provider", providerID, "error", e)
		h.fail(w, r, "oauth_failed")
		return
	}
	ext, err := p.Exchange(r.Context(), r.URL.Query().Get("code"), st.Verifier, st.Nonce, h.callbackURL(providerID))
	if err != nil {
		h.log.WarnContext(r.Context(), "login exchange failed", "provider", providerID, "err", err)
		h.fail(w, r, "oauth_failed")
		return
	}
	m := h.meta(r)
	m.Invite = st.Invite
	res, err := h.svc.CompleteLogin(r.Context(), ext, m)
	if err != nil {
		if e := apperr.As(err); e.Kind == apperr.KindForbidden {
			if e.Code == DenyAccountDisabled {
				// Spaces as %20 (not "+") so any query parser reads them back.
				target := "/login?error=" + url.QueryEscape(e.Code)
				for _, k := range []string{"reason", "until"} {
					if v, ok := e.Details[k].(string); ok && v != "" {
						target += "&" + k + "=" + strings.ReplaceAll(url.QueryEscape(v), "+", "%20")
					}
				}
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
			h.fail(w, r, e.Code)
			return
		}
		h.log.ErrorContext(r.Context(), "complete login", "err", err)
		h.fail(w, r, "oauth_failed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: res.Token, Path: "/", Expires: res.ExpiresAt,
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, st.Redirect, http.StatusFound)
}

func (h *Handler) readState(r *http.Request) (*oauthState, error) {
	c, err := r.Cookie(stateCookie)
	if err != nil {
		return nil, err
	}
	raw, err := h.stateBox.Open(c.Value, stateCookie)
	if err != nil {
		return nil, err
	}
	var st oauthState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	if time.Now().Unix() > st.Expires {
		return nil, errors.New("state expired")
	}
	return &st, nil
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(code), http.StatusFound)
}

func (h *Handler) meta(r *http.Request) RequestMeta {
	return RequestMeta{
		UserAgent: r.UserAgent(),
		IPPrefix:  httpx.IPPrefix(httpx.ClientIP(r.Context())),
		RequestID: httpx.RequestID(r.Context()),
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), PrincipalFrom(r.Context()), h.meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r.Context())
	u, err := h.svc.store.Get(r.Context(), p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := map[string]any{"user": u, "permissions": authz.Permissions(u.Role)}
	if h.MeGroup != nil {
		g, err := h.MeGroup(r.Context(), u.ID)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out["group"] = g
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListSessions(r.Context(), PrincipalFrom(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("会话"))
		return
	}
	if err := h.svc.RevokeSession(r.Context(), PrincipalFrom(r.Context()), id, h.meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) revokeOthers(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.RevokeOtherSessions(r.Context(), PrincipalFrom(r.Context()), h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// RequireSession authenticates the session cookie and rejects anonymous requests.
func (h *Handler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var token string
		if c, err := r.Cookie(SessionCookie); err == nil {
			token = c.Value
		}
		p, _, err := h.svc.Authenticate(r.Context(), token)
		if err != nil {
			httpx.WriteError(w, r, apperr.Internal(err))
			return
		}
		if p == nil {
			httpx.WriteError(w, r, apperr.Unauthenticated())
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// OptionalSession authenticates the session cookie when there is one and
// passes anonymous requests through without a principal (public endpoints
// whose answer depends on who asks, e.g. the model plaza).
func (h *Handler) OptionalSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		p, _, err := h.svc.Authenticate(r.Context(), c.Value)
		if err != nil {
			httpx.WriteError(w, r, apperr.Internal(err))
			return
		}
		if p != nil {
			r = r.WithContext(WithPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

// Require rejects principals lacking perm.
func Require(perm authz.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !PrincipalFrom(r.Context()).Can(perm) {
				httpx.WriteError(w, r, apperr.Forbidden())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRF protects cookie-authenticated unsafe requests: they must carry
// X-Requested-With (a non-simple header browsers won't send cross-site without
// CORS preflight) and, when present, an allowed Origin.
func CSRF(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range allowedOrigins {
		allowed[strings.TrimRight(o, "/")] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
				httpx.WriteError(w, r, apperr.New(apperr.KindForbidden, "csrf_rejected", "缺少 CSRF 防护请求头"))
				return
			}
			if o := r.Header.Get("Origin"); o != "" && !allowed[o] {
				httpx.WriteError(w, r, apperr.New(apperr.KindForbidden, "csrf_rejected", "请求来源不被允许"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SafeRedirect only allows same-site absolute paths ("/x"), never "//host" or
// "/\host" which browsers treat as protocol-relative.
// DefaultRedirect is where users land after login when no (valid) target is
// given: the console. "/" is the public landing page.
const DefaultRedirect = "/console"

func SafeRedirect(s string) string {
	// Only same-origin absolute paths. Backslashes are rejected anywhere:
	// browsers treat "\" like "/", and http.Redirect cleans "/./\x" into
	// "/\x", which would become a protocol-relative URL to host x.
	if s == "" || len(s) > 2048 || !strings.HasPrefix(s, "/") || strings.ContainsRune(s, '\\') ||
		strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return DefaultRedirect
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return DefaultRedirect
	}
	// Validate the normalized form (the one the browser will follow).
	clean := path.Clean(u.Path)
	if strings.HasSuffix(u.Path, "/") && clean != "/" {
		clean += "/"
	}
	if !strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "//") || clean == "/api" || strings.HasPrefix(clean, "/api/") {
		return DefaultRedirect
	}
	out := (&url.URL{Path: clean, RawQuery: u.RawQuery, Fragment: u.Fragment}).String()
	if !strings.HasPrefix(out, "/") || strings.HasPrefix(out, "//") {
		return DefaultRedirect
	}
	return out
}

func randString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// limiter is a per-key token bucket for login endpoints (in-process; a Redis
// implementation replaces it when running multiple instances).
type limiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	buckets map[string]*rate.Limiter
	sweep   time.Time
}

func newLimiter(every rate.Limit, burst int) *limiter {
	return &limiter{every: every, burst: burst, buckets: map[string]*rate.Limiter{}, sweep: time.Now()}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.sweep) > 10*time.Minute {
		for k, b := range l.buckets {
			if b.Tokens() >= float64(l.burst) {
				delete(l.buckets, k)
			}
		}
		l.sweep = time.Now()
	}
	b, ok := l.buckets[key]
	if !ok {
		b = rate.NewLimiter(l.every, l.burst)
		l.buckets[key] = b
	}
	return b.Allow()
}
