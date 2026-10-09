package transfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"omnigate/internal/app"
	"omnigate/internal/config"
	"omnigate/internal/platform/db"
)

// End-to-end: a source database is populated through the real application
// (users, sessions, channels with encrypted credentials, keys, prices,
// wallets and ledger, redeem codes, plans, subscriptions and quota usage,
// request logs in two months, plugins with versions and storage,
// notifications, settings with an encrypted SMTP password), migrated with
// Run, and the application is started on the target and checked through its
// API: logins and existing sessions work, the channel credential decrypts
// (the fake upstream receives it), balances, ledger, logs, statistics and
// quota usage are unchanged, and the plugin's storage continues.
//
// With a PostgreSQL test server the data goes SQLite → PostgreSQL →
// SQLite; otherwise SQLite → SQLite.

const upstreamKey = "sk-upstream-secret-0123456789"

type world struct {
	t         *testing.T
	gh        *httptest.Server
	up        *httptest.Server
	lastAuth  atomic.Value // Authorization header of the last upstream call
	masterKey string
	from, to  string // stats window
}

func newWorld(t *testing.T) *world {
	w := &world{t: t}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	w.masterKey = "k1:" + base64.StdEncoding.EncodeToString(raw)
	now := time.Now().UTC()
	w.from, w.to = now.AddDate(0, 0, -60).Format(time.RFC3339), now.Add(24*time.Hour).Format(time.RFC3339)
	w.gh = fakeGitHub(t)
	w.up = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.lastAuth.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer "+upstreamKey {
			http.Error(rw, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		fmt.Fprint(rw, `{"id":"c1","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	}))
	t.Cleanup(w.up.Close)
	return w
}

func fakeGitHub(t *testing.T) *httptest.Server {
	users := map[string]int64{"alice": 1001, "bob": 1002, "carol": 1003}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
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
		_ = json.NewEncoder(w).Encode(map[string]any{"id": users[k], "login": k, "name": strings.ToUpper(k)})
	})
	mux.HandleFunc("GET /user/emails", func(w http.ResponseWriter, r *http.Request) {
		k, ok := who(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"email": k + "@example.com", "primary": true, "verified": true}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// node is the application running on one database.
type node struct {
	app     *app.App
	srv     *httptest.Server
	pool    *db.DB
	stopped bool
	cancel  context.CancelFunc
}

func (w *world) start(dsn string) *node {
	w.t.Helper()
	ctx := context.Background()
	env := map[string]string{
		"OMNIGATE_DATABASE_URL": dsn, "OMNIGATE_DATA_DIR": w.t.TempDir(), "OMNIGATE_PUBLIC_URL": "http://localhost:8080", "OMNIGATE_COOKIE_SECURE": "false",
		"OMNIGATE_REGISTRATION_MODE": "open", "OMNIGATE_BOOTSTRAP_ADMINS": "github:alice", "OMNIGATE_MASTER_KEY": w.masterKey,
		"OMNIGATE_AUTH_GITHUB_CLIENT_ID": "gh-client", "OMNIGATE_AUTH_GITHUB_CLIENT_SECRET": "gh-secret",
		"OMNIGATE_AUTH_GITHUB_AUTH_URL": w.gh.URL + "/login/oauth/authorize", "OMNIGATE_AUTH_GITHUB_TOKEN_URL": w.gh.URL + "/login/oauth/access_token",
		"OMNIGATE_AUTH_GITHUB_API_URL": w.gh.URL, "OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK": "true",
	}
	cfg, err := config.LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		w.t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool, log); err != nil {
		w.t.Fatal(err)
	}
	a, err := app.New(ctx, cfg, log, pool, app.Options{})
	if err != nil {
		w.t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	a.Start(wctx)
	n := &node{app: a, srv: httptest.NewServer(a.Handler()), pool: pool, cancel: cancel}
	w.t.Cleanup(n.stop)
	return n
}

// stop shuts the application down and closes its database (the service is
// stopped before migrating, as documented).
func (n *node) stop() {
	if n.stopped {
		return
	}
	n.stopped = true
	n.srv.Close()
	n.cancel()
	n.app.Stop()
	n.pool.Close()
}

func (n *node) settle(t *testing.T) {
	t.Helper()
	if err := n.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	n.app.Notifications().Wait()
}

// client is a browser session; its cookie jar survives switching to another
// node (cookies are per host, not per port), which tests session migration.
type client struct {
	t    *testing.T
	c    *http.Client
	base string
}

func (w *world) client(n *node) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: w.t, base: n.srv.URL, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func (c *client) do(method, path string, body any) (int, map[string]any, http.Header) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, resp.Header
}

func (c *client) must(method, path string, body any, want int) map[string]any {
	c.t.Helper()
	code, out, _ := c.do(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s = %d (want %d): %v", method, path, code, want, out)
	}
	return out
}

func (c *client) login(user string) {
	c.t.Helper()
	code, _, h := c.do(http.MethodGet, "/api/auth/github/login?redirect=/", nil)
	if code != http.StatusFound {
		c.t.Fatalf("login = %d", code)
	}
	authURL, _ := url.Parse(h.Get("Location"))
	code, _, _ = c.do(http.MethodGet, "/api/auth/github/callback?code="+user+"&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
	if code != http.StatusFound {
		c.t.Fatalf("callback = %d", code)
	}
}

func (c *client) userID() string {
	c.t.Helper()
	return c.must(http.MethodGet, "/api/me", nil, 200)["user"].(map[string]any)["id"].(string)
}

// gateway sends a chat completion with an API key.
func (c *client) gateway(key, model string) (int, string) {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.base+"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model)))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const storePlugin = `import { definePlugin } from "@omnigate/plugin-sdk"
export default definePlugin({
  capabilities: {
    "custom.count"() { const n = (og.storage.get("n") ?? 0) + 1; og.storage.set("n", n); return { n } },
  },
})`

func storeFiles() map[string]string {
	return map[string]string{
		"manifest.json": `{"id":"acme.store","name":"计数","version":"0.1.0","sdk":1,"extends":"openai.chat","entry":"src/index.ts",
		  "defaults":{"models":[{"model":"stored","upstreamModel":"up-stored"}]},
		  "configSchema":{"type":"object","properties":{}},
		  "permissions":{"network":["$baseUrl"],"secrets":["apiKey"],"schedule":[],"storage":{"maxKeys":10,"maxBytes":1000}},
		  "capabilities":{"custom.count":{"output":"json","userTriggerable":true,"timeout":"2s"}},
		  "hooks":[],"uiContributions":[]}`,
		"src/index.ts": storePlugin,
	}
}

// actors are the clients and ids used across nodes.
type actors struct {
	admin, carol      *client
	adminID, carolID  string
	carolKey          string
	pluginChannel     string
	used, count, logs int // expected subscription usage, plugin counter, carol's request logs
}

// populate fills the source through the API.
func (w *world) populate(n *node) *actors {
	t := w.t
	a := &actors{admin: w.client(n), carol: w.client(n)}
	a.admin.login("alice")
	a.carol.login("carol")
	bob := w.client(n)
	bob.login("bob")
	a.adminID, a.carolID = a.admin.userID(), a.carol.userID()

	a.admin.must(http.MethodPost, "/api/channels", map[string]any{"name": "oa", "type": "openai", "baseUrl": w.up.URL + "/v1",
		"apiKey": upstreamKey, "scope": "global", "models": []map[string]string{{"model": "m1", "upstreamModel": "up-m1"}, {"model": "m2", "upstreamModel": "up-m2"}}}, 201)
	for _, m := range []string{"m1", "m2"} {
		a.admin.must(http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": m, "inputPerM": "1000", "outputPerM": "1000"}, 201)
	}
	st := a.admin.must(http.MethodGet, "/api/admin/settings", nil, 200)
	a.admin.must(http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"billing": map[string]any{"enforce": true},
		"notifications": map[string]any{"smtp": map[string]any{"host": "smtp.example.com", "port": 2525, "security": "none",
			"username": "mailer", "password": "smtp-pa55-secret", "from": "OmniGate <noreply@example.com>"}}}}, 200)

	// Wallet: an admin credit and a redeemed code.
	wallet := a.admin.must(http.MethodGet, "/api/admin/billing/wallets/"+a.carolID, nil, 200)
	a.admin.must(http.MethodPost, "/api/admin/billing/wallets/"+a.carolID+"/adjust", map[string]any{"amount": "10", "note": "迁移测试", "version": wallet["version"]}, 200)
	batch := a.admin.must(http.MethodPost, "/api/admin/billing/redeem-batches", map[string]any{"kind": "wallet_credit", "amount": "5", "count": 2}, 201)
	a.carol.must(http.MethodPost, "/api/billing/redeem", map[string]any{"code": batch["codes"].([]any)[0]}, 200)

	// A plan covering m1, granted to carol.
	plan := a.admin.must(http.MethodPost, "/api/admin/billing/plans", map[string]any{
		"name": "测试套餐", "description": "", "duration": "30d", "models": []string{"m1"}, "stackable": false,
		"rules": []map[string]any{{"id": "5h", "label": "5 小时", "meter": "requests", "window": map[string]any{"kind": "session", "duration": "5h"}, "limit": "10"}},
	}, 201)
	a.admin.must(http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": a.carolID, "planId": plan["id"], "periods": 1}, 201)

	// Gateway traffic: m1 on the subscription, m2 from the wallet.
	key := a.carol.must(http.MethodPost, "/api/keys", map[string]any{"name": "ck"}, 201)
	a.carolKey = key["secret"].(string)
	time.Sleep(100 * time.Millisecond) // channel registry reload
	for _, m := range []string{"m1", "m1", "m1", "m2", "m2"} {
		if code, raw := a.carol.gateway(a.carolKey, m); code != 200 {
			t.Fatalf("gateway %s = %d %s", m, code, raw)
		}
		n.settle(t)
	}
	a.used, a.logs = 3, 5

	// Plugin with storage.
	pl := a.admin.must(http.MethodPost, "/api/plugins", map[string]any{"id": "acme.store", "name": "计数", "template": "blank"}, 201)
	pid := pl["id"].(string)
	a.admin.must(http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": storeFiles(), "version": 1}, 200)
	v := a.admin.must(http.MethodPost, "/api/plugins/"+pid+"/draft/publish", nil, 201)
	if v["approval"] != "approved" {
		a.admin.must(http.MethodPost, "/api/plugins/"+pid+"/versions/"+v["id"].(string)+"/approve", map[string]any{"decision": "approve"}, 200)
	}
	ch := a.admin.must(http.MethodPost, "/api/channels", map[string]any{"name": "stored", "pluginVersionId": v["id"],
		"baseUrl": w.up.URL + "/v1", "apiKey": upstreamKey}, 201)
	a.pluginChannel = ch["id"].(string)
	for i := 1; i <= 2; i++ {
		a.countPlugin(i)
	}

	// Notification preferences with an encrypted webhook secret.
	a.carol.must(http.MethodPut, "/api/notifications/webhook/secret", map[string]any{"secret": "hush"}, 200)
	n.settle(t)

	// Spread request logs over two months.
	ctx := context.Background()
	rows, err := n.pool.Query(ctx, `SELECT id FROM request_logs ORDER BY started_at LIMIT 2`)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := db.CollectRows[string](rows)
	if err != nil || len(ids) != 2 {
		t.Fatalf("logs = %v %v", ids, err)
	}
	for _, id := range ids {
		if _, err := n.pool.Exec(ctx, `UPDATE request_logs SET started_at = $1 WHERE id = $2`, time.Now().UTC().AddDate(0, 0, -35), id); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func (a *actors) countPlugin(want int) {
	a.admin.t.Helper()
	res := a.admin.must(http.MethodPost, "/api/channels/"+a.pluginChannel+"/capabilities/custom.count", nil, 200)
	out, _ := res["output"].(map[string]any)
	if res["ok"] != true || out == nil || out["n"] != float64(want) {
		a.admin.t.Fatalf("plugin counter = %v, want %d", res, want)
	}
	a.count = want
}

// snapshot reads what the API reports about the migrated data.
func (w *world) snapshot(a *actors) map[string]any {
	q := "from=" + url.QueryEscape(w.from) + "&to=" + url.QueryEscape(w.to)
	out := map[string]any{}
	for _, g := range []struct {
		c    *client
		path string
	}{
		{a.carol, "/api/me"},
		{a.carol, "/api/billing/wallet"},
		{a.carol, "/api/billing/ledger"},
		{a.carol, "/api/billing/subscriptions"},
		{a.carol, "/api/logs?pageSize=100&" + q},
		{a.carol, "/api/stats/summary?" + q},
		{a.carol, "/api/notifications?pageSize=100"},
		{a.carol, "/api/notifications/preferences"},
		{a.carol, "/api/keys"},
		{a.admin, "/api/stats/summary?" + q},
		{a.admin, "/api/admin/billing/wallets/" + a.carolID},
		{a.admin, "/api/admin/billing/redeem-batches"},
		{a.admin, "/api/admin/billing/plans"},
		{a.admin, "/api/admin/prices"},
		{a.admin, "/api/admin/users"},
		{a.admin, "/api/plugins"},
	} {
		code, body, _ := g.c.do(http.MethodGet, g.path, nil)
		if code != 200 {
			w.t.Fatalf("GET %s = %d %v", g.path, code, body)
		}
		who := "carol"
		if g.c == a.admin {
			who = "admin"
		}
		out[who+" GET "+g.path] = normalizeJSON(body)
	}
	settings := a.admin.must(http.MethodGet, "/api/admin/settings", nil, 200)
	out["smtp"] = normalizeJSON(settings["settings"].(map[string]any)["notifications"])
	return out
}

// normalizeJSON truncates timestamps to microseconds (PostgreSQL's precision).
func normalizeJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, e := range x {
			m[k] = normalizeJSON(e)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = normalizeJSON(e)
		}
		return s
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil && len(x) > 10 {
			return t.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
		}
	}
	return v
}

func (w *world) compare(before, after map[string]any) {
	w.t.Helper()
	for k, b := range before {
		if !reflect.DeepEqual(b, after[k]) {
			bj, _ := json.Marshal(b)
			aj, _ := json.Marshal(after[k])
			w.t.Errorf("%s changed:\n  before: %s\n  after:  %s", k, bj, aj)
		}
	}
}

// migrate stops nothing itself: the caller stops the source node first.
func (w *world) migrate(from, to string) {
	w.t.Helper()
	var out bytes.Buffer
	if _, err := Run(context.Background(), Options{From: from, To: to, Batch: 3, Out: &out}); err != nil {
		w.t.Fatalf("migrate-db %s → %s: %v\n%s", from, to, err, out.String())
	}
	w.t.Logf("migrate-db output:\n%s", out.String())
	sameData(w.t, from, to)
}

// verify starts checks on the node the actors now point at.
func (w *world) verify(n *node, a *actors, before map[string]any) {
	t := w.t
	a.admin.base, a.carol.base = n.srv.URL, n.srv.URL
	// Existing sessions (cookies) still work and the data reads the same.
	w.compare(before, w.snapshot(a))

	// A fresh login maps to the same account and role.
	alice := w.client(n)
	alice.login("alice")
	me := alice.must(http.MethodGet, "/api/me", nil, 200)["user"].(map[string]any)
	if me["id"] != a.adminID || me["role"] != "system_admin" {
		t.Fatalf("alice after migration = %v", me)
	}

	// The channel credential decrypts: the upstream receives it. m2 is billed.
	balance := a.carol.must(http.MethodGet, "/api/billing/wallet", nil, 200)["balance"].(string)
	w.lastAuth.Store("")
	if code, raw := a.carol.gateway(a.carolKey, "m2"); code != 200 {
		t.Fatalf("gateway after migration = %d %s", code, raw)
	}
	if got := w.lastAuth.Load(); got != "Bearer "+upstreamKey {
		t.Fatalf("upstream saw %q", got)
	}
	n.settle(t)
	wallet := a.carol.must(http.MethodGet, "/api/billing/wallet", nil, 200)
	if want := subDecimal(t, balance, "0.012"); wallet["balance"] != want {
		t.Fatalf("balance %s → %v, want %s", balance, wallet["balance"], want)
	}
	// m1 is covered by the migrated subscription; its usage continues.
	if code, raw := a.carol.gateway(a.carolKey, "m1"); code != 200 {
		t.Fatalf("covered request = %d %s", code, raw)
	}
	n.settle(t)
	a.used++
	a.logs += 2
	subs := a.carol.must(http.MethodGet, "/api/billing/subscriptions", nil, 200)
	rule := subs["items"].([]any)[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if rule["used"] != fmt.Sprint(a.used) {
		t.Fatalf("subscription usage = %v, want %d", rule["used"], a.used)
	}
	q := "from=" + url.QueryEscape(w.from) + "&to=" + url.QueryEscape(w.to)
	if s := a.carol.must(http.MethodGet, "/api/stats/summary?"+q, nil, 200); fmt.Sprint(s["totals"].(map[string]any)["requests"]) != fmt.Sprint(a.logs) {
		t.Fatalf("stats = %v, want %d requests", s, a.logs)
	}
	// The plugin's storage carries over.
	a.countPlugin(a.count + 1)
}

func subDecimal(t *testing.T, a, b string) string {
	t.Helper()
	ra, ok1 := new(big.Rat).SetString(a)
	rb, ok2 := new(big.Rat).SetString(b)
	if !ok1 || !ok2 {
		t.Fatalf("bad decimals %q %q", a, b)
	}
	return normalizeDecimal(ra.Sub(ra, rb).FloatString(9))
}

func TestMigrateDBEndToEnd(t *testing.T) {
	requireDB(t)
	w := newWorld(t)
	srcURL := sqliteURL(t)
	src := w.start(srcURL)
	a := w.populate(src)
	before := w.snapshot(a)
	src.stop()

	if !havePostgres() {
		dst := sqliteURL(t)
		w.migrate(srcURL, dst)
		w.verify(w.start(dst), a, before)
		return
	}

	// SQLite → PostgreSQL.
	pg := pgURL(t)
	w.migrate(srcURL, pg)
	checkPartitions(t, pg)
	n1 := w.start(pg)
	w.verify(n1, a, before)
	mid := w.snapshot(a)
	n1.stop()

	// PostgreSQL → SQLite (round trip).
	back := sqliteURL(t)
	w.migrate(pg, back)
	w.verify(w.start(back), a, mid)
}

// checkPartitions: the copied request logs live in monthly partitions, none
// in the DEFAULT partition.
func checkPartitions(t *testing.T, u string) {
	t.Helper()
	d := openRaw(t, u)
	ctx := context.Background()
	var parts, inDefault int64
	if err := d.QueryRow(ctx, `SELECT count(DISTINCT tableoid) FROM request_logs WHERE tableoid <> 'request_logs_default'::regclass`).Scan(&parts); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(ctx, `SELECT count(*) FROM request_logs_default`).Scan(&inDefault); err != nil {
		t.Fatal(err)
	}
	if parts != 2 || inDefault != 0 {
		t.Fatalf("request logs in %d monthly partitions, %d in the default partition", parts, inDefault)
	}
}
