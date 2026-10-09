// Package config loads OmniGate runtime configuration from environment variables.
//
// All variables use the OMNIGATE_ prefix. See .env.example at the repository root
// for the full list with descriptions.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Env string

const (
	EnvDevelopment Env = "development"
	EnvProduction  Env = "production"
)

type RegistrationMode string

const (
	// RegistrationOpen lets any successfully authenticated identity create an account.
	RegistrationOpen RegistrationMode = "open"
	// RegistrationRestricted only admits bootstrap admins and allowlisted identities.
	RegistrationRestricted RegistrationMode = "restricted"
	// RegistrationClosed admits only identities already linked to an existing account.
	RegistrationClosed RegistrationMode = "closed"
)

type Config struct {
	Env         Env
	HTTPAddr    string
	MetricsAddr string
	PublicURL   *url.URL
	// AllowedOrigins are extra browser origins accepted by the CSRF origin check
	// (the PublicURL origin is always allowed).
	AllowedOrigins []string
	TrustedProxies []netip.Prefix

	DatabaseURL string
	RedisURL    string // reserved: read but not used yet (multi-instance limits/counters, Phase 3–4)
	AutoMigrate bool
	// SeedOnStart applies a catalog once per database after migrations:
	// "" (off), "builtin" (the catalog embedded in the binary) or a file path
	// (OMNIGATE_SEED_ON_START, deploy/seed/README.md).
	SeedOnStart string

	// ChannelsAllowPrivateNetwork lets channels owned by channel managers reach
	// loopback/private addresses (local Ollama, intranet gateways).
	ChannelsAllowPrivateNetwork bool
	// UpstreamProxy routes all upstream traffic through an HTTP(S) proxy.
	UpstreamProxy *url.URL
	// PluginHeapLimitMB is the plugin memory watchdog threshold (process heap growth).
	PluginHeapLimitMB int

	// WebEnabled serves the web app (SPA) on the main port; WebDir serves it from
	// a directory instead of the build embedded in the binary.
	WebEnabled bool
	WebDir     string

	// DataDir holds local state that must survive restarts: the settlement
	// journal (ADR-0010). Default /data in production (the container volume)
	// and ./.data in development; created (0700) when missing.
	DataDir string

	MasterKeys []MasterKey

	LogLevel  string
	LogFormat string

	Currency string

	Auth AuthConfig

	// SMTP is the environment layer of the notifications.smtp.* system
	// settings (docs/contracts/phase6-api.md §1); empty fields are unset.
	SMTP SMTPEnv

	// Warnings are non-fatal problems found while loading (e.g. a
	// github:<login> bootstrap admin in production). Load has no logger, so
	// the caller logs them.
	Warnings []string
}

// SMTPEnv holds OMNIGATE_SMTP_* values; zero values mean "not set".
type SMTPEnv struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
}

type MasterKey struct {
	ID  string
	Key []byte
}

type AuthConfig struct {
	RegistrationMode    RegistrationMode
	BootstrapAdmins     []IdentityMatcher
	AllowedIdentities   []IdentityMatcher
	AllowedEmailDomains []string
	SessionTTL          time.Duration
	SessionIdleTTL      time.Duration
	CookieSecure        bool

	GitHub *GitHubProvider
	OIDC   []OIDCProvider
}

// IdentityMatcher matches an external identity, e.g. "github:octocat" (login),
// "github-id:583231" (immutable numeric id) or "<oidc-provider-id>:<subject>".
type IdentityMatcher struct {
	Provider string
	Value    string
}

type GitHubProvider struct {
	ClientID     string
	ClientSecret string
	// Endpoint overrides are used by tests and GitHub Enterprise Server.
	AuthURL  string
	TokenURL string
	APIURL   string
}

type OIDCProvider struct {
	ID           string
	DisplayName  string
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

func (c *Config) IsProduction() bool { return c.Env == EnvProduction }

// Load reads configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads configuration using the supplied lookup function (for tests).
func LoadFrom(getenv func(string) string) (*Config, error) {
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv("OMNIGATE_" + k)); v != "" {
			return v
		}
		return def
	}
	var errs []error

	c := &Config{
		Env:         Env(get("ENV", string(EnvDevelopment))),
		HTTPAddr:    get("HTTP_ADDR", ":8080"),
		MetricsAddr: get("METRICS_ADDR", ":9090"),
		DatabaseURL: get("DATABASE_URL", ""),
		RedisURL:    get("REDIS_URL", ""),
		LogLevel:    get("LOG_LEVEL", "info"),
		LogFormat:   get("LOG_FORMAT", "json"),
		Currency:    strings.ToUpper(get("CURRENCY", "USD")),
	}
	if c.Env != EnvDevelopment && c.Env != EnvProduction {
		errs = append(errs, fmt.Errorf("OMNIGATE_ENV must be development or production, got %q", c.Env))
	}
	if c.DatabaseURL == "" {
		// The container image defaults it to sqlite:///data/omnigate.db.
		errs = append(errs, errors.New("OMNIGATE_DATABASE_URL is required (postgres://… or sqlite:///path/to/omnigate.db)"))
	}
	if !isCurrencyCode(c.Currency) {
		errs = append(errs, fmt.Errorf("OMNIGATE_CURRENCY must be an ISO 4217 code (3 letters), got %q", c.Currency))
	}

	pub, err := url.Parse(get("PUBLIC_URL", "http://localhost:8080"))
	if err != nil || (pub.Scheme != "http" && pub.Scheme != "https") || pub.Host == "" {
		errs = append(errs, fmt.Errorf("OMNIGATE_PUBLIC_URL must be an absolute http(s) URL"))
	} else {
		pub.Path = strings.TrimRight(pub.Path, "/")
		if pub.Path != "" || pub.RawQuery != "" || pub.Fragment != "" {
			errs = append(errs, errors.New("OMNIGATE_PUBLIC_URL must be an origin without path (subpath deployment is not supported)"))
		}
		c.PublicURL = pub
		if c.IsProduction() && pub.Scheme != "https" {
			errs = append(errs, errors.New("OMNIGATE_PUBLIC_URL must use https in production"))
		}
	}
	c.AllowedOrigins = splitList(get("ALLOWED_ORIGINS", ""))

	for _, p := range splitList(get("TRUSTED_PROXIES", "")) {
		prefix, err := netip.ParsePrefix(p)
		if err != nil && !strings.Contains(p, "/") {
			var addr netip.Addr
			if addr, err = netip.ParseAddr(p); err == nil {
				addr = addr.Unmap()
				prefix = netip.PrefixFrom(addr, addr.BitLen())
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("OMNIGATE_TRUSTED_PROXIES: %w", err))
			continue
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix)
	}

	keys, err := parseMasterKeys(get("MASTER_KEY", ""))
	if err != nil {
		errs = append(errs, err)
	}
	c.MasterKeys = keys

	c.Auth, err = loadAuth(get, c)
	if err != nil {
		errs = append(errs, err)
	}
	if c.IsProduction() && !c.Auth.CookieSecure {
		errs = append(errs, errors.New("OMNIGATE_COOKIE_SECURE cannot be false in production"))
	}
	if c.IsProduction() {
		// Checked here, before the database is opened or migrated, so a
		// misconfigured production deployment changes nothing
		// (internal/app.validate repeats the key and bootstrap checks).
		if len(c.MasterKeys) == 0 && get("MASTER_KEY", "") == "" {
			errs = append(errs, errors.New("OMNIGATE_MASTER_KEY is required in production (generate one with `omnigate keygen`)"))
		}
		if get("BOOTSTRAP_ADMINS", "") == "" {
			errs = append(errs, errors.New("OMNIGATE_BOOTSTRAP_ADMINS is required in production (e.g. github-id:12345)"))
		}
		if get("AUTH_GITHUB_CLIENT_ID", "") == "" && get("AUTH_OIDC", "") == "" {
			errs = append(errs, errors.New("no login provider configured: production requires OMNIGATE_AUTH_GITHUB_CLIENT_ID/_SECRET or OMNIGATE_AUTH_OIDC (nobody could sign in)"))
		}
	}
	errs = append(errs, checkBootstrapAdmins(c)...)
	if c.AutoMigrate, err = strconv.ParseBool(get("AUTO_MIGRATE", "true")); err != nil {
		errs = append(errs, fmt.Errorf("OMNIGATE_AUTO_MIGRATE: %w", err))
	}
	if c.ChannelsAllowPrivateNetwork, err = strconv.ParseBool(get("CHANNELS_ALLOW_PRIVATE_NETWORK", "false")); err != nil {
		errs = append(errs, fmt.Errorf("OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK: %w", err))
	}
	if c.WebEnabled, err = strconv.ParseBool(get("WEB_ENABLED", "true")); err != nil {
		errs = append(errs, fmt.Errorf("OMNIGATE_WEB_ENABLED: %w", err))
	}
	c.WebDir = get("WEB_DIR", "")
	c.SeedOnStart = strings.TrimSpace(get("SEED_ON_START", ""))
	if strings.EqualFold(c.SeedOnStart, "off") || strings.EqualFold(c.SeedOnStart, "false") {
		c.SeedOnStart = ""
	}
	defaultDataDir := "./.data"
	if c.IsProduction() {
		defaultDataDir = "/data"
	}
	c.DataDir = get("DATA_DIR", defaultDataDir)
	if c.PluginHeapLimitMB, err = strconv.Atoi(get("PLUGIN_HEAP_LIMIT_MB", "256")); err != nil || c.PluginHeapLimitMB < 16 || c.PluginHeapLimitMB > 8192 {
		errs = append(errs, errors.New("OMNIGATE_PLUGIN_HEAP_LIMIT_MB must be an integer between 16 and 8192"))
	}
	if v := get("UPSTREAM_PROXY", ""); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") || u.Host == "" {
			errs = append(errs, errors.New("OMNIGATE_UPSTREAM_PROXY must be an http(s):// or socks5:// URL"))
		} else {
			c.UpstreamProxy = u
		}
	}

	c.SMTP = SMTPEnv{Host: get("SMTP_HOST", ""), Security: strings.ToLower(get("SMTP_SECURITY", "")), Username: get("SMTP_USERNAME", ""),
		Password: getenv("OMNIGATE_SMTP_PASSWORD"), From: get("SMTP_FROM", "")}
	if v := get("SMTP_PORT", ""); v != "" {
		if c.SMTP.Port, err = strconv.Atoi(v); err != nil || c.SMTP.Port < 1 || c.SMTP.Port > 65535 {
			errs = append(errs, errors.New("OMNIGATE_SMTP_PORT must be an integer between 1 and 65535"))
		}
	}
	switch c.SMTP.Security {
	case "", "starttls", "tls":
	case "none":
		if c.IsProduction() {
			errs = append(errs, errors.New("OMNIGATE_SMTP_SECURITY=none is not allowed in production"))
		}
	default:
		errs = append(errs, errors.New("OMNIGATE_SMTP_SECURITY must be starttls, tls or none"))
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

func loadAuth(get func(string, string) string, c *Config) (AuthConfig, error) {
	var errs []error
	a := AuthConfig{
		RegistrationMode:    RegistrationMode(get("REGISTRATION_MODE", string(RegistrationRestricted))),
		AllowedEmailDomains: splitList(strings.ToLower(get("AUTH_ALLOWED_EMAIL_DOMAINS", ""))),
	}
	switch a.RegistrationMode {
	case RegistrationOpen, RegistrationRestricted, RegistrationClosed:
	default:
		errs = append(errs, fmt.Errorf("OMNIGATE_REGISTRATION_MODE must be open, restricted or closed"))
	}
	var err error
	if a.BootstrapAdmins, err = parseMatchers(get("BOOTSTRAP_ADMINS", "")); err != nil {
		errs = append(errs, fmt.Errorf("OMNIGATE_BOOTSTRAP_ADMINS: %w", err))
	}
	if a.AllowedIdentities, err = parseMatchers(get("AUTH_ALLOWED_IDENTITIES", "")); err != nil {
		errs = append(errs, fmt.Errorf("OMNIGATE_AUTH_ALLOWED_IDENTITIES: %w", err))
	}
	if a.SessionTTL, err = time.ParseDuration(get("SESSION_TTL", "720h")); err != nil {
		errs = append(errs, fmt.Errorf("OMNIGATE_SESSION_TTL: %w", err))
	}
	if a.SessionIdleTTL, err = time.ParseDuration(get("SESSION_IDLE_TTL", "168h")); err != nil {
		errs = append(errs, fmt.Errorf("OMNIGATE_SESSION_IDLE_TTL: %w", err))
	}
	secure := c.PublicURL == nil || c.PublicURL.Scheme == "https"
	if v := get("COOKIE_SECURE", ""); v != "" {
		if secure, err = strconv.ParseBool(v); err != nil {
			errs = append(errs, fmt.Errorf("OMNIGATE_COOKIE_SECURE: %w", err))
		}
	}
	a.CookieSecure = secure

	if id := get("AUTH_GITHUB_CLIENT_ID", ""); id != "" {
		a.GitHub = &GitHubProvider{
			ClientID:     id,
			ClientSecret: get("AUTH_GITHUB_CLIENT_SECRET", ""),
			AuthURL:      get("AUTH_GITHUB_AUTH_URL", "https://github.com/login/oauth/authorize"),
			TokenURL:     get("AUTH_GITHUB_TOKEN_URL", "https://github.com/login/oauth/access_token"),
			APIURL:       strings.TrimRight(get("AUTH_GITHUB_API_URL", "https://api.github.com"), "/"),
		}
		if a.GitHub.ClientSecret == "" {
			errs = append(errs, errors.New("OMNIGATE_AUTH_GITHUB_CLIENT_SECRET is required when GitHub login is enabled"))
		}
		for _, e := range [][2]string{{"AUTH_GITHUB_AUTH_URL", a.GitHub.AuthURL}, {"AUTH_GITHUB_TOKEN_URL", a.GitHub.TokenURL}, {"AUTH_GITHUB_API_URL", a.GitHub.APIURL}} {
			if err := checkEndpoint(e[1], c.IsProduction()); err != nil {
				errs = append(errs, fmt.Errorf("OMNIGATE_%s %w", e[0], err))
			}
		}
	}

	// OMNIGATE_AUTH_OIDC=dev,corp enables providers configured by
	// OMNIGATE_AUTH_OIDC_<ID>_{ISSUER,CLIENT_ID,CLIENT_SECRET,NAME,SCOPES}.
	for _, id := range splitList(get("AUTH_OIDC", "")) {
		id = strings.ToLower(id)
		if id == "github" || id == "email" || strings.HasSuffix(id, "-id") || !validProviderID(id) {
			errs = append(errs, fmt.Errorf("invalid OIDC provider id %q (lowercase letters, digits and '-'; not 'github', 'email' or ending in '-id')", id))
			continue
		}
		p := "AUTH_OIDC_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_"
		prov := OIDCProvider{
			ID:           id,
			DisplayName:  get(p+"NAME", id),
			Issuer:       get(p+"ISSUER", ""),
			ClientID:     get(p+"CLIENT_ID", ""),
			ClientSecret: get(p+"CLIENT_SECRET", ""),
			Scopes:       splitList(get(p+"SCOPES", "openid,profile,email")),
		}
		if prov.Issuer == "" || prov.ClientID == "" {
			errs = append(errs, fmt.Errorf("OIDC provider %q requires OMNIGATE_%sISSUER and OMNIGATE_%sCLIENT_ID", id, p, p))
			continue
		}
		// An http:// issuer would fetch the signing keys and send the client
		// secret and authorization code in clear text: refused in production.
		if err := checkEndpoint(prov.Issuer, c.IsProduction()); err != nil {
			errs = append(errs, fmt.Errorf("OMNIGATE_%sISSUER %w", p, err))
			continue
		}
		a.OIDC = append(a.OIDC, prov)
	}
	return a, errors.Join(errs...)
}

// checkEndpoint validates an identity-provider URL: absolute http(s), and
// https only in production.
func checkEndpoint(raw string, production bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an absolute http(s) URL")
	}
	if production && u.Scheme != "https" {
		return errors.New("must use https in production")
	}
	return nil
}

// checkBootstrapAdmins catches bootstrap admin lists that can never match a
// configured login provider (a typo would leave production without an
// administrator) and, in production, warns about github:<login> entries:
// GitHub logins can be renamed and re-registered by someone else, while
// github-id:<id> is immutable.
func checkBootstrapAdmins(c *Config) []error {
	admins := c.Auth.BootstrapAdmins
	if len(admins) == 0 {
		return nil
	}
	usable := 0
	for _, m := range admins {
		if c.Auth.matcherUsable(m) {
			usable++
		} else {
			c.Warnings = append(c.Warnings, fmt.Sprintf("OMNIGATE_BOOTSTRAP_ADMINS entry %s:%s refers to a login provider that is not configured", m.Provider, m.Value))
		}
		if m.Provider == "github" && c.IsProduction() {
			c.Warnings = append(c.Warnings, fmt.Sprintf("OMNIGATE_BOOTSTRAP_ADMINS entry github:%s matches a renameable GitHub login; prefer the immutable github-id:<numeric id> (curl -s https://api.github.com/users/%s)", m.Value, m.Value))
		}
	}
	hasProvider := c.Auth.GitHub != nil || len(c.Auth.OIDC) > 0
	if usable == 0 && hasProvider && c.IsProduction() {
		return []error{errors.New("OMNIGATE_BOOTSTRAP_ADMINS: no entry matches a configured login provider (use github-id:<id> with GitHub, <oidc-id>:<sub> with OIDC, or email:<verified address>)")}
	}
	return nil
}

// matcherUsable reports whether m can match an identity from an enabled
// provider (see auth.matchAny).
func (a *AuthConfig) matcherUsable(m IdentityMatcher) bool {
	switch m.Provider {
	case "email":
		return a.GitHub != nil || len(a.OIDC) > 0
	case "github", "github-id":
		return a.GitHub != nil
	}
	for _, p := range a.OIDC {
		if m.Provider == p.ID || m.Provider == p.ID+"-id" {
			return true
		}
	}
	return false
}

func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func validProviderID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// parseMasterKeys parses "kid1:base64key,kid2:base64key". The first key is the
// active encryption key; the rest are kept for decrypting older data.
func parseMasterKeys(s string) ([]MasterKey, error) {
	var keys []MasterKey
	seen := map[string]bool{}
	for _, item := range splitList(s) {
		kid, b64, ok := strings.Cut(item, ":")
		if !ok || kid == "" {
			return nil, errors.New("OMNIGATE_MASTER_KEY entries must look like <kid>:<base64 32 bytes>")
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("OMNIGATE_MASTER_KEY %q must be 32 bytes, base64 encoded", kid)
		}
		if seen[kid] {
			return nil, fmt.Errorf("OMNIGATE_MASTER_KEY: duplicate kid %q", kid)
		}
		seen[kid] = true
		keys = append(keys, MasterKey{ID: kid, Key: raw})
	}
	return keys, nil
}

func parseMatchers(s string) ([]IdentityMatcher, error) {
	var out []IdentityMatcher
	for _, item := range splitList(s) {
		prov, val, ok := strings.Cut(item, ":")
		if !ok || prov == "" || val == "" {
			return nil, fmt.Errorf("entry %q must look like <provider>:<value>", item)
		}
		prov = strings.ToLower(prov)
		if prov == "github-id" {
			if _, err := strconv.ParseUint(val, 10, 64); err != nil {
				return nil, fmt.Errorf("entry %q: github-id takes the numeric GitHub user id (curl -s https://api.github.com/users/<login> | grep '\"id\"')", item)
			}
		}
		out = append(out, IdentityMatcher{Provider: prov, Value: val})
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
