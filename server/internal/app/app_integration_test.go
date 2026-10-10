package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"omnigate/internal/app"
	"omnigate/internal/config"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
)

// testDB returns the URL of an isolated database for this test (PostgreSQL or
// SQLite, see dbtest). Requires OMNIGATE_TEST_DATABASE_URL (skips otherwise).
func testDB(t *testing.T) string {
	t.Helper()
	return dbtest.URL(t)
}

// fakeGitHub emulates GitHub's OAuth token endpoint and REST user API. The
// authorization code doubles as the user key.
func fakeGitHub(t *testing.T) *httptest.Server {
	users := map[string]struct {
		id    int64
		login string
		email string
	}{
		"alice": {1001, "alice", "alice@example.com"},
		"bob":   {1002, "bob", "bob@other.org"},
		"carol": {1003, "carol", "carol@example.com"},
		"dave":  {1004, "dave", "dave@example.com"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code_verifier") == "" || r.Form.Get("client_secret") != "gh-secret" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok-%s","token_type":"bearer","scope":"read:user,user:email"}`, r.Form.Get("code"))
	})
	who := func(r *http.Request) (string, bool) {
		k := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer tok-")
		_, ok := users[k]
		return k, ok
	}
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		k, ok := who(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		u := users[k]
		_ = json.NewEncoder(w).Encode(map[string]any{"id": u.id, "login": u.login, "name": strings.ToUpper(u.login), "avatar_url": "https://avatars.githubusercontent.com/u/1"})
	})
	mux.HandleFunc("GET /user/emails", func(w http.ResponseWriter, r *http.Request) {
		k, ok := who(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"email": users[k].email, "primary": true, "verified": true}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type harness struct {
	t   *testing.T
	srv *httptest.Server
}

type client struct {
	h *harness
	c *http.Client
}

func (h *harness) newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{h: h, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func (c *client) do(method, path string, body any, headers ...string) (*http.Response, map[string]any) {
	c.h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.h.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := c.c.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

// login runs the full redirect dance and returns the final redirect location.
func (c *client) login(code string) string {
	c.h.t.Helper()
	resp, _ := c.do(http.MethodGet, "/api/auth/github/login?redirect=/admin/users", nil)
	if resp.StatusCode != http.StatusFound {
		c.h.t.Fatalf("login status %d", resp.StatusCode)
	}
	authURL, _ := url.Parse(resp.Header.Get("Location"))
	q := authURL.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
		c.h.t.Fatalf("authorize url missing PKCE/state: %s", authURL)
	}
	resp, _ = c.do(http.MethodGet, "/api/auth/github/callback?code="+code+"&state="+url.QueryEscape(q.Get("state")), nil)
	if resp.StatusCode != http.StatusFound {
		c.h.t.Fatalf("callback status %d", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

func TestAuthAndAdminFlow(t *testing.T) {
	dsn := testDB(t)
	gh := fakeGitHub(t)
	ctx := context.Background()

	env := map[string]string{
		"OMNIGATE_DATABASE_URL":              dsn,
		"OMNIGATE_DATA_DIR":                  t.TempDir(),
		"OMNIGATE_PUBLIC_URL":                "http://localhost:8080",
		"OMNIGATE_COOKIE_SECURE":             "false",
		"OMNIGATE_REGISTRATION_MODE":         "restricted",
		"OMNIGATE_BOOTSTRAP_ADMINS":          "github:Alice",
		"OMNIGATE_AUTH_ALLOWED_IDENTITIES":   "github-id:1003",
		"OMNIGATE_AUTH_GITHUB_CLIENT_ID":     "gh-client",
		"OMNIGATE_AUTH_GITHUB_CLIENT_SECRET": "gh-secret",
		"OMNIGATE_AUTH_GITHUB_AUTH_URL":      gh.URL + "/login/oauth/authorize",
		"OMNIGATE_AUTH_GITHUB_TOKEN_URL":     gh.URL + "/login/oauth/access_token",
		"OMNIGATE_AUTH_GITHUB_API_URL":       gh.URL,
		"OMNIGATE_CURRENCY":                  "CNY",
	}
	cfg, err := config.LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, cfg, log, pool, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	h := &harness{t: t, srv: srv}

	anon := h.newClient()
	if resp, _ := anon.do(http.MethodGet, "/api/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /api/me = %d", resp.StatusCode)
	}
	_, info := anon.do(http.MethodGet, "/api/system/info", nil)
	if info["currency"].(map[string]any)["code"] != "CNY" {
		t.Fatalf("currency not persisted: %v", info)
	}
	_, provs := anon.do(http.MethodGet, "/api/auth/providers", nil)
	if fmt.Sprint(provs["providers"]) != "[map[displayName:GitHub id:github type:github]]" {
		t.Fatalf("providers = %v", provs)
	}

	t.Run("state mismatch is rejected", func(t *testing.T) {
		c := h.newClient()
		c.do(http.MethodGet, "/api/auth/github/login", nil)
		resp, _ := c.do(http.MethodGet, "/api/auth/github/callback?code=alice&state=forged", nil)
		if loc := resp.Header.Get("Location"); loc != "/login?error=state_mismatch" {
			t.Fatalf("location = %q", loc)
		}
	})

	t.Run("unknown provider redirects to login page", func(t *testing.T) {
		resp, _ := h.newClient().do(http.MethodGet, "/api/auth/nope/login", nil)
		if loc := resp.Header.Get("Location"); loc != "/login?error=unknown_provider" {
			t.Fatalf("location = %q", loc)
		}
	})

	t.Run("open redirect is neutralized", func(t *testing.T) {
		// "/./\\evil.com" is the security audit's bypass: http.Redirect cleaned it
		// into "/\\evil.com", which browsers follow to https://evil.com/.
		for i, target := range []string{"//evil.example", "/./%5Cevil.com", "/x/../api/auth/logout"} {
			c := h.newClient()
			resp, _ := c.do(http.MethodGet, "/api/auth/github/login?redirect="+target, nil)
			authURL, _ := url.Parse(resp.Header.Get("Location"))
			resp, _ = c.do(http.MethodGet, "/api/auth/github/callback?code=alice&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
			if loc := resp.Header.Get("Location"); loc != "/console" {
				t.Fatalf("redirect %q → location %q", target, loc)
			}
			if i > 0 { // the sessions subtest below expects exactly one session from here
				c.do(http.MethodPost, "/api/auth/logout", nil)
			}
		}
	})

	admin := h.newClient()
	if loc := admin.login("alice"); loc != "/admin/users" {
		t.Fatalf("admin redirect = %q", loc)
	}
	_, me := admin.do(http.MethodGet, "/api/me", nil)
	if me["user"].(map[string]any)["role"] != "system_admin" {
		t.Fatalf("bootstrap admin role = %v", me)
	}

	if loc := h.newClient().login("bob"); loc != "/login?error=not_allowed" {
		t.Fatalf("bob (not allowlisted) redirect = %q", loc)
	}

	carol := h.newClient()
	if loc := carol.login("carol"); loc != "/admin/users" {
		t.Fatalf("carol redirect = %q", loc)
	}
	_, me = carol.do(http.MethodGet, "/api/me", nil)
	carolUser := me["user"].(map[string]any)
	if carolUser["role"] != "user" || carolUser["email"] != "carol@example.com" {
		t.Fatalf("carol = %v", carolUser)
	}
	carolID := carolUser["id"].(string)

	if resp, _ := carol.do(http.MethodGet, "/api/admin/users", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain user listing users = %d", resp.StatusCode)
	}

	_, list := admin.do(http.MethodGet, "/api/admin/users?q=car", nil)
	if list["total"].(float64) != 1 {
		t.Fatalf("search = %v", list)
	}

	resp, body := admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"role": "auditor"}, "X-Requested-With", "")
	if resp.StatusCode != http.StatusForbidden || body["error"].(map[string]any)["code"] != "csrf_rejected" {
		t.Fatalf("missing CSRF header = %d %v", resp.StatusCode, body)
	}
	resp, _ = admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"role": "auditor", "version": 1}, "Origin", "https://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin = %d", resp.StatusCode)
	}
	resp, _ = admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"role": "auditor", "version": 99})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale version = %d", resp.StatusCode)
	}
	resp, _ = admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"version": 1})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty patch = %d", resp.StatusCode)
	}
	resp, body = admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"role": "superuser", "version": 1})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid role = %d %v", resp.StatusCode, body)
	}
	resp, body = admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"role": "auditor", "version": 1})
	if resp.StatusCode != http.StatusOK || body["version"].(float64) != 2 {
		t.Fatalf("patch = %d %v", resp.StatusCode, body)
	}

	_, me = admin.do(http.MethodGet, "/api/me", nil)
	adminID := me["user"].(map[string]any)["id"].(string)
	resp, body = admin.do(http.MethodPatch, "/api/admin/users/"+adminID, map[string]any{"role": "user", "version": 1})
	if resp.StatusCode != http.StatusConflict || body["error"].(map[string]any)["code"] != "last_admin" {
		t.Fatalf("demoting last admin = %d %v", resp.StatusCode, body)
	}

	// Disabling a user invalidates their sessions immediately.
	// (phase7-api.md §2.1: a reason is required; the login page shows it.)
	resp, _ = admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"status": "disabled", "disabledReason": "abuse", "version": 2})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable = %d", resp.StatusCode)
	}
	if resp, _ := carol.do(http.MethodGet, "/api/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled user still authenticated: %d", resp.StatusCode)
	}
	if loc := h.newClient().login("carol"); loc != "/login?error=account_disabled&reason=abuse" {
		t.Fatalf("disabled re-login = %q", loc)
	}

	// Sessions: alice also has the session from the open-redirect subtest and a
	// second device; revoke the others, then log out.
	other := h.newClient()
	other.login("alice")
	_, sess := admin.do(http.MethodGet, "/api/me/sessions", nil)
	if n := len(sess["items"].([]any)); n != 3 {
		t.Fatalf("sessions = %d", n)
	}
	_, rv := admin.do(http.MethodPost, "/api/me/sessions/revoke-others", nil)
	if rv["revoked"].(float64) != 2 {
		t.Fatalf("revoke-others = %v", rv)
	}
	if resp, _ := other.do(http.MethodGet, "/api/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("revoked session still valid")
	}

	_, logs := admin.do(http.MethodGet, "/api/admin/audit-logs?pageSize=100", nil)
	seen := map[string]bool{}
	for _, it := range logs["items"].([]any) {
		seen[it.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"auth.login", "auth.login_denied", "user.create", "user.update", "auth.session_revoke_others"} {
		if !seen[want] {
			t.Errorf("audit log missing %s (have %v)", want, seen)
		}
	}

	if resp, _ := admin.do(http.MethodPost, "/api/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if resp, _ := admin.do(http.MethodGet, "/api/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("session valid after logout")
	}

	// The data plane rejects missing keys in each protocol's error format.
	resp, body = anon.do(http.MethodPost, "/v1/chat/completions", map[string]any{})
	if resp.StatusCode != http.StatusUnauthorized || body["error"].(map[string]any)["code"] != "invalid_api_key" {
		t.Fatalf("openai unauthenticated = %d %v", resp.StatusCode, body)
	}
	resp, body = anon.do(http.MethodPost, "/v1/messages", map[string]any{})
	if resp.StatusCode != http.StatusUnauthorized || body["type"] != "error" || body["error"].(map[string]any)["type"] != "authentication_error" {
		t.Fatalf("anthropic unauthenticated = %d %v", resp.StatusCode, body)
	}
	if resp.Header.Get("X-OmniGate-Request-Id") == "" {
		t.Fatal("missing request id header")
	}

	// SPA fallback serves index.html for client routes.
	if resp, _ := anon.do(http.MethodGet, "/admin/users", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("spa fallback = %d", resp.StatusCode)
	}
}
