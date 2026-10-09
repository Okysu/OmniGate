package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != EnvDevelopment || c.HTTPAddr != ":8080" || c.Currency != "USD" ||
		c.Auth.RegistrationMode != RegistrationRestricted || c.Auth.CookieSecure {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.DataDir != "./.data" {
		t.Fatalf("development data dir = %q", c.DataDir)
	}
}

// prodEnv is a minimal valid production configuration.
func prodEnv() map[string]string {
	return map[string]string{
		"OMNIGATE_DATABASE_URL":              "postgres://x",
		"OMNIGATE_ENV":                       "production",
		"OMNIGATE_PUBLIC_URL":                "https://gw.example.com",
		"OMNIGATE_MASTER_KEY":                "k1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"OMNIGATE_BOOTSTRAP_ADMINS":          "github-id:583231",
		"OMNIGATE_AUTH_GITHUB_CLIENT_ID":     "gh",
		"OMNIGATE_AUTH_GITHUB_CLIENT_SECRET": "secret",
	}
}

func TestDataDir(t *testing.T) {
	prod := prodEnv()
	c, err := LoadFrom(env(prod))
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/data" {
		t.Fatalf("production data dir = %q", c.DataDir)
	}
	prod["OMNIGATE_DATA_DIR"] = "/var/lib/omnigate"
	if c, _ = LoadFrom(env(prod)); c.DataDir != "/var/lib/omnigate" {
		t.Fatalf("explicit data dir = %q", c.DataDir)
	}
}

func TestOIDCProviders(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{
		"OMNIGATE_DATABASE_URL":                 "postgres://x",
		"OMNIGATE_AUTH_OIDC":                    "Corp-SSO",
		"OMNIGATE_AUTH_OIDC_CORP_SSO_ISSUER":    "https://id.example.com",
		"OMNIGATE_AUTH_OIDC_CORP_SSO_CLIENT_ID": "abc",
		"OMNIGATE_AUTH_OIDC_CORP_SSO_NAME":      "公司 SSO",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Auth.OIDC) != 1 || c.Auth.OIDC[0].ID != "corp-sso" || c.Auth.OIDC[0].DisplayName != "公司 SSO" {
		t.Fatalf("oidc = %+v", c.Auth.OIDC)
	}
}

func TestValidationErrors(t *testing.T) {
	_, err := LoadFrom(env(map[string]string{
		"OMNIGATE_ENV":                   "production",
		"OMNIGATE_PUBLIC_URL":            "http://gw.example.com",
		"OMNIGATE_MASTER_KEY":            "k1:short",
		"OMNIGATE_AUTH_GITHUB_CLIENT_ID": "x",
		"OMNIGATE_AUTH_OIDC":             "github",
		"OMNIGATE_BOOTSTRAP_ADMINS":      "nocolon",
	}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"DATABASE_URL", "https in production", "32 bytes", "CLIENT_SECRET", "invalid OIDC provider id", "BOOTSTRAP_ADMINS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

func TestHardening(t *testing.T) {
	base := map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", "OMNIGATE_TRUSTED_PROXIES": "10.0.0.1, 172.16.0.0/12"}
	c, err := LoadFrom(env(base))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 2 || c.TrustedProxies[0].String() != "10.0.0.1/32" || !c.AutoMigrate {
		t.Fatalf("proxies = %v autoMigrate = %v", c.TrustedProxies, c.AutoMigrate)
	}
	bad := map[string]string{
		"OMNIGATE_CURRENCY":     "US1",
		"OMNIGATE_PUBLIC_URL":   "https://example.com/gw",
		"OMNIGATE_AUTH_OIDC":    "email,corp-id",
		"OMNIGATE_AUTO_MIGRATE": "nope",
	}
	for k, v := range base {
		bad[k] = v
	}
	_, err = LoadFrom(env(bad))
	for _, want := range []string{"ISO 4217", "without path", `"email"`, `"corp-id"`, "AUTO_MIGRATE"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	_, err = LoadFrom(env(map[string]string{
		"OMNIGATE_DATABASE_URL": "postgres://x", "OMNIGATE_ENV": "production",
		"OMNIGATE_PUBLIC_URL": "https://gw.example.com", "OMNIGATE_COOKIE_SECURE": "false",
	}))
	if err == nil || !strings.Contains(err.Error(), "COOKIE_SECURE") {
		t.Errorf("insecure cookie allowed in production: %v", err)
	}
}

func TestWebSettings(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.WebEnabled || c.WebDir != "" {
		t.Fatalf("web defaults: enabled=%v dir=%q", c.WebEnabled, c.WebDir)
	}
	c, err = LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", "OMNIGATE_WEB_ENABLED": "false", "OMNIGATE_WEB_DIR": "/srv/web"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.WebEnabled || c.WebDir != "/srv/web" {
		t.Fatalf("web overrides: enabled=%v dir=%q", c.WebEnabled, c.WebDir)
	}
	if _, err := LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", "OMNIGATE_WEB_ENABLED": "maybe"})); err == nil || !strings.Contains(err.Error(), "OMNIGATE_WEB_ENABLED") {
		t.Fatalf("invalid OMNIGATE_WEB_ENABLED accepted: %v", err)
	}
}

func TestSMTPEnv(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", "OMNIGATE_SMTP_HOST": "smtp.example.com",
		"OMNIGATE_SMTP_PORT": "465", "OMNIGATE_SMTP_SECURITY": "TLS", "OMNIGATE_SMTP_USERNAME": "u", "OMNIGATE_SMTP_PASSWORD": " p w ",
		"OMNIGATE_SMTP_FROM": "OmniGate <noreply@example.com>"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.SMTP != (SMTPEnv{Host: "smtp.example.com", Port: 465, Security: "tls", Username: "u", Password: " p w ", From: "OmniGate <noreply@example.com>"}) {
		t.Fatalf("smtp = %+v", c.SMTP)
	}
	for k, v := range map[string]string{"OMNIGATE_SMTP_PORT": "0", "OMNIGATE_SMTP_SECURITY": "ssl"} {
		if _, err := LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", k: v})); err == nil || !strings.Contains(err.Error(), k) {
			t.Fatalf("%s=%s: %v", k, v, err)
		}
	}
	_, err = LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", "OMNIGATE_ENV": "production",
		"OMNIGATE_PUBLIC_URL": "https://gw.example.com", "OMNIGATE_SMTP_SECURITY": "none"}))
	if err == nil || !strings.Contains(err.Error(), "OMNIGATE_SMTP_SECURITY=none") {
		t.Fatalf("production none = %v", err)
	}
}

func TestProductionRequirements(t *testing.T) {
	c, err := LoadFrom(env(prodEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", c.Warnings)
	}
	// Missing master key, bootstrap admins and login provider are all
	// reported by Load, i.e. before the database is opened or migrated.
	_, err = LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "sqlite:///data/omnigate.db", "OMNIGATE_ENV": "production",
		"OMNIGATE_PUBLIC_URL": "https://gw.example.com"}))
	for _, want := range []string{"OMNIGATE_MASTER_KEY is required", "OMNIGATE_BOOTSTRAP_ADMINS is required", "no login provider configured"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	// Development keeps working without them (ephemeral key, first-user admin).
	if _, err := LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x"})); err != nil {
		t.Fatal(err)
	}
	// An OIDC provider alone satisfies the login requirement.
	m := prodEnv()
	delete(m, "OMNIGATE_AUTH_GITHUB_CLIENT_ID")
	delete(m, "OMNIGATE_AUTH_GITHUB_CLIENT_SECRET")
	m["OMNIGATE_AUTH_OIDC"] = "corp"
	m["OMNIGATE_AUTH_OIDC_CORP_ISSUER"] = "https://id.example.com"
	m["OMNIGATE_AUTH_OIDC_CORP_CLIENT_ID"] = "omnigate"
	m["OMNIGATE_BOOTSTRAP_ADMINS"] = "corp:248289761001"
	if _, err := LoadFrom(env(m)); err != nil {
		t.Fatal(err)
	}
}

func TestProviderEndpointsHTTPS(t *testing.T) {
	m := prodEnv()
	m["OMNIGATE_AUTH_OIDC"] = "corp"
	m["OMNIGATE_AUTH_OIDC_CORP_ISSUER"] = "http://id.example.com"
	m["OMNIGATE_AUTH_OIDC_CORP_CLIENT_ID"] = "omnigate"
	m["OMNIGATE_AUTH_GITHUB_API_URL"] = "http://ghes.example.com/api/v3"
	_, err := LoadFrom(env(m))
	for _, want := range []string{"OMNIGATE_AUTH_OIDC_CORP_ISSUER must use https in production", "OMNIGATE_AUTH_GITHUB_API_URL must use https in production"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	// Development allows http (local mock IdP), but not garbage.
	dev := map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", "OMNIGATE_AUTH_OIDC": "dev",
		"OMNIGATE_AUTH_OIDC_DEV_ISSUER": "http://localhost:9000/default", "OMNIGATE_AUTH_OIDC_DEV_CLIENT_ID": "c"}
	if _, err := LoadFrom(env(dev)); err != nil {
		t.Fatal(err)
	}
	dev["OMNIGATE_AUTH_OIDC_DEV_ISSUER"] = "localhost:9000"
	if _, err := LoadFrom(env(dev)); err == nil || !strings.Contains(err.Error(), "absolute http(s) URL") {
		t.Fatalf("relative issuer accepted: %v", err)
	}
}

func TestBootstrapAdminChecks(t *testing.T) {
	// github:<login> in production: allowed, with a warning.
	m := prodEnv()
	m["OMNIGATE_BOOTSTRAP_ADMINS"] = "github:octocat"
	c, err := LoadFrom(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "github-id") {
		t.Fatalf("warnings = %v", c.Warnings)
	}
	// Development: no warning for github:<login>.
	c, err = LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", "OMNIGATE_BOOTSTRAP_ADMINS": "github:octocat",
		"OMNIGATE_AUTH_GITHUB_CLIENT_ID": "gh", "OMNIGATE_AUTH_GITHUB_CLIENT_SECRET": "s"}))
	if err != nil || len(c.Warnings) != 0 {
		t.Fatalf("dev github:<login>: err=%v warnings=%v", err, c.Warnings)
	}
	// github-id must be numeric (any environment, both matcher lists).
	for _, k := range []string{"OMNIGATE_BOOTSTRAP_ADMINS", "OMNIGATE_AUTH_ALLOWED_IDENTITIES"} {
		_, err := LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", k: "github-id:octocat"}))
		if err == nil || !strings.Contains(err.Error(), "numeric GitHub user id") {
			t.Errorf("%s non-numeric github-id: %v", k, err)
		}
	}
	// No entry can match a configured provider: refused in production.
	m = prodEnv()
	m["OMNIGATE_BOOTSTRAP_ADMINS"] = "corp:123"
	if _, err := LoadFrom(env(m)); err == nil || !strings.Contains(err.Error(), "no entry matches a configured login provider") {
		t.Fatalf("unmatchable bootstrap admins accepted: %v", err)
	}
	// Some entries unusable: warning only.
	m["OMNIGATE_BOOTSTRAP_ADMINS"] = "corp:123,github-id:583231,email:admin@example.com"
	c, err = LoadFrom(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "corp:123") {
		t.Fatalf("warnings = %v", c.Warnings)
	}
	// <oidc-id>-id:<sub> is an accepted alias of <oidc-id>:<sub>.
	m = prodEnv()
	m["OMNIGATE_AUTH_OIDC"] = "corp"
	m["OMNIGATE_AUTH_OIDC_CORP_ISSUER"] = "https://id.example.com"
	m["OMNIGATE_AUTH_OIDC_CORP_CLIENT_ID"] = "omnigate"
	m["OMNIGATE_BOOTSTRAP_ADMINS"] = "corp-id:abc"
	if c, err = LoadFrom(env(m)); err != nil || len(c.Warnings) != 0 {
		t.Fatalf("corp-id alias: err=%v warnings=%v", err, c.Warnings)
	}
}

func TestSeedOnStart(t *testing.T) {
	for in, want := range map[string]string{"": "", "  ": "", "off": "", "FALSE": "", "builtin": "builtin", " /data/catalog.json ": "/data/catalog.json"} {
		c, err := LoadFrom(env(map[string]string{"OMNIGATE_DATABASE_URL": "postgres://x", "OMNIGATE_SEED_ON_START": in}))
		if err != nil {
			t.Fatal(err)
		}
		if c.SeedOnStart != want {
			t.Errorf("OMNIGATE_SEED_ON_START=%q → %q, want %q", in, c.SeedOnStart, want)
		}
	}
}
