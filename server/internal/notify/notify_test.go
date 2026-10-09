package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/notify/notifytest"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
	"omnigate/internal/platform/secretbox"
	"omnigate/internal/settings"
)

// fakeSettings is a static Settings source.
type fakeSettings struct {
	mu      sync.Mutex
	smtp    settings.SMTPConfig
	enabled bool
	limit   int
}

func (f *fakeSettings) SMTP(context.Context) settings.SMTPConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.smtp
}
func (f *fakeSettings) NotificationsEnabled(context.Context) bool { return f.enabled }
func (f *fakeSettings) EmailRateLimitPerHour(context.Context) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.limit
}
func (f *fakeSettings) SiteName(context.Context) string { return "Acme AI" }

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t    *testing.T
	pool *db.DB
	svc  *Service
	set  *fakeSettings
	smtp *notifytest.Server
	clk  *clock
}

func newEnv(t *testing.T) *env {
	pool := dbtest.Open(t)
	srv := notifytest.Start(t, "none")
	set := &fakeSettings{enabled: true, limit: 20,
		smtp: settings.SMTPConfig{Host: srv.Host, Port: srv.Port, Security: "none", From: "Acme <noreply@acme.test>"}}
	clk := &clock{t: time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := New(Options{Pool: pool, Log: log, Audit: audit.NewRecorder(pool, log), Settings: set,
		Keys: []secretbox.Key{{ID: "k1", Raw: make([]byte, 32)}}, PublicURL: &url.URL{Scheme: "https", Host: "gw.example.com"},
		WebhookTransport: http.DefaultTransport, AllowPrivateNetwork: true, Now: clk.now, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, pool: pool, svc: svc, set: set, smtp: srv, clk: clk}
}

// user creates an active user with a verified account email.
func (e *env) user(name string, role identity.Role) uuid.UUID {
	e.t.Helper()
	id := uuid.Must(uuid.NewV7())
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := e.pool.Exec(ctx, `INSERT INTO users (id, display_name, email, role, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $5)`,
		id, name, name+"@example.com", role, now); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO user_identities (id, user_id, provider, subject, email, email_verified, created_at)
		VALUES ($1, $2, 'github', $3, $4, true, $5)`, uuid.Must(uuid.NewV7()), id, name, name+"@example.com", now); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) principal(id uuid.UUID, role identity.Role) *authz.Principal {
	return &authz.Principal{UserID: id, Name: "u", Role: role}
}

func (e *env) emit(ev Event) bool {
	e.t.Helper()
	ok, err := e.svc.Emit(context.Background(), ev)
	if err != nil {
		e.t.Fatal(err)
	}
	return ok
}

func (e *env) process() int {
	e.t.Helper()
	total := 0
	for {
		n, err := e.svc.ProcessDue(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		total += n
		if n == 0 {
			return total
		}
	}
}

func (e *env) statuses(uid uuid.UUID) map[string]int {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT kind || ':' || status FROM notification_deliveries WHERE user_id = $1`, uid)
	if err != nil {
		e.t.Fatal(err)
	}
	list, err := db.CollectRows[string](rows)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]int{}
	for _, s := range list {
		out[s]++
	}
	return out
}

func (e *env) putPrefs(uid uuid.UUID, role identity.Role, body string) *Preferences {
	e.t.Helper()
	var in PreferencesInput
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		e.t.Fatal(err)
	}
	p, err := e.svc.PutPreferences(context.Background(), e.principal(uid, role), in, RequestMeta{})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func ev(t, key string, users ...uuid.UUID) Event {
	return Event{Type: t, Key: key, Title: "标题 " + key, Body: "正文 " + key, Link: "/console/billing", Users: users}
}

func TestEmitDedupeAndInApp(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice", identity.RoleUser)
	if !e.emit(ev(TypeWalletCredited, "k1", u)) || e.emit(ev(TypeWalletCredited, "k1", u)) {
		t.Fatal("dedupe key must emit once")
	}
	// A fresh service (other instance, empty memory cache) still dedupes in the database.
	svc2, _ := New(e.svc.opts)
	if ok, err := svc2.Emit(context.Background(), ev(TypeWalletCredited, "k1", u)); err != nil || ok {
		t.Fatalf("db dedupe = %v %v", ok, err)
	}
	items, total, err := e.svc.List(context.Background(), u, ListQuery{Limit: 10})
	if err != nil || total != 1 || items[0].Title != "标题 k1" || items[0].Severity != "info" || items[0].ReadAt != nil {
		t.Fatalf("list = %v %d %v", items, total, err)
	}
	// wallet.credited: email off by default, in-app on.
	if st := e.statuses(u); len(st) != 0 {
		t.Fatalf("deliveries = %v", st)
	}
	// Ineligible recipients (plugin.pending_approval needs plugins.trust) get nothing.
	e.emit(ev(TypePluginPendingApproval, "p1", u))
	if n, _ := e.svc.UnreadCount(context.Background(), u); n != 1 {
		t.Fatalf("unread = %d", n)
	}
}

func TestEmailDeliveryUnsubscribeAndRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user("bob", identity.RoleUser)
	e.smtp.FailNext.Store(1)
	e.emit(ev(TypeSubscriptionExpiring, "s1", u))
	e.process()
	if st := e.statuses(u); st["event:pending"] != 1 {
		t.Fatalf("after failure = %v", st)
	}
	var attempts int
	var next time.Time
	_ = e.pool.QueryRow(ctx, `SELECT attempts, next_attempt_at FROM notification_deliveries WHERE user_id = $1`, u).Scan(&attempts, &next)
	if attempts != 1 || !next.Equal(e.clk.now().Add(time.Minute)) {
		t.Fatalf("backoff = %d %v", attempts, next)
	}
	e.clk.add(time.Minute)
	e.process()
	msgs := e.smtp.Wait(1, time.Second)
	if len(msgs) != 1 || msgs[0].To[0] != "bob@example.com" || msgs[0].Subject != "[Acme AI] 标题 s1" ||
		!strings.Contains(msgs[0].Text, "正文 s1") || !strings.Contains(msgs[0].HTML, "https://gw.example.com/console/billing") {
		t.Fatalf("mail = %+v", msgs)
	}
	unsub := msgs[0].Header.Get("List-Unsubscribe")
	if !strings.HasPrefix(unsub, "<https://gw.example.com/api/notifications/unsubscribe?token=") {
		t.Fatalf("List-Unsubscribe = %q", unsub)
	}
	link, _ := url.Parse(strings.Trim(unsub, "<>"))
	for i := 0; i < 2; i++ { // idempotent
		typ, err := e.svc.Unsubscribe(ctx, link.Query().Get("token"))
		if err != nil || typ != TypeSubscriptionExpiring {
			t.Fatalf("unsubscribe = %q %v", typ, err)
		}
	}
	p, _ := e.svc.GetPreferences(ctx, e.principal(u, identity.RoleUser))
	if p.Events[TypeSubscriptionExpiring].Email || !p.Events[TypeSubscriptionExpiring].InApp || p.Version != 1 {
		t.Fatalf("prefs after unsubscribe = %+v", p)
	}
	if _, err := e.svc.Unsubscribe(ctx, "v1:k1:garbage"); err == nil {
		t.Fatal("bad token accepted")
	}
	// No more emails of that type.
	e.emit(ev(TypeSubscriptionExpiring, "s2", u))
	if st := e.statuses(u); st["event:sent"] != 1 || st["event:pending"] != 0 {
		t.Fatalf("after unsubscribe = %v", st)
	}

	// Permanent failures stop after 5 attempts.
	e.emit(ev(TypeKeyExpiring, "k-fail", u))
	e.smtp.FailNext.Store(10)
	for i := 0; i < 6; i++ {
		e.process()
		e.clk.add(time.Hour)
	}
	if st := e.statuses(u); st["event:failed"] != 1 {
		t.Fatalf("give up = %v", st)
	}
}

func TestDigestGrouping(t *testing.T) {
	e := newEnv(t)
	u := e.user("carol", identity.RoleUser)
	e.putPrefs(u, identity.RoleUser, `{"version":0,"digest":"daily","timezone":"Asia/Shanghai"}`)
	// 02:00 UTC = 10:00 Asia/Shanghai: the digest goes out tomorrow 09:00 (01:00 UTC).
	e.emit(ev(TypeSubscriptionExpiring, "d1", u))
	e.emit(ev(TypeKeyExpiring, "d2", u))
	e.emit(ev(TypeModelPriceChanged, "d3", u))
	e.emit(ev(TypeWalletBalanceLow, "alert", u)) // alert-class: never digested
	e.process()
	if msgs := e.smtp.Wait(1, time.Second); len(msgs) != 1 || msgs[0].Subject != "[Acme AI] 标题 alert" {
		t.Fatalf("immediate = %+v", msgs)
	}
	var due time.Time
	_ = e.pool.QueryRow(context.Background(), `SELECT min(next_attempt_at) FROM notification_deliveries WHERE kind = 'digest'`).Scan(&due)
	if want := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC); !due.Equal(want) {
		t.Fatalf("digest due = %v, want %v", due, want)
	}
	e.clk.add(23*time.Hour + time.Minute)
	e.process()
	msgs := e.smtp.Wait(2, time.Second)
	if len(msgs) != 2 || !strings.Contains(msgs[1].Subject, "每日通知摘要") ||
		!strings.Contains(msgs[1].Text, "标题 d1") || !strings.Contains(msgs[1].Text, "标题 d2") || !strings.Contains(msgs[1].Text, "标题 d3") {
		t.Fatalf("digest = %+v", msgs)
	}
	if st := e.statuses(u); st["digest:sent"] != 3 {
		t.Fatalf("statuses = %v", st)
	}
}

func TestEmailRateLimitSummary(t *testing.T) {
	e := newEnv(t)
	e.set.limit = 2
	u := e.user("dave", identity.RoleUser)
	for _, k := range []string{"r1", "r2", "r3", "r4", "r5"} {
		e.emit(ev(TypeKeyExpiring, k, u))
		e.clk.add(time.Second)
	}
	e.process()
	if msgs := e.smtp.Wait(2, time.Second); len(msgs) != 2 {
		t.Fatalf("sent = %d", len(msgs))
	}
	if st := e.statuses(u); st["event:sent"] != 2 || st["event:held"] != 3 || st["summary:pending"] != 1 {
		t.Fatalf("statuses = %v", st)
	}
	e.process() // the summary is not due yet
	if len(e.smtp.Messages()) != 2 {
		t.Fatal("summary sent early")
	}
	e.clk.add(time.Hour)
	e.process()
	msgs := e.smtp.Wait(3, time.Second)
	if len(msgs) != 3 || !strings.Contains(msgs[2].Subject, "3 条通知摘要") || !strings.Contains(msgs[2].Text, "标题 r5") ||
		strings.Contains(msgs[2].Text, "标题 r1") {
		t.Fatalf("summary = %+v", msgs)
	}
	if st := e.statuses(u); st["event:merged"] != 3 || st["summary:sent"] != 1 {
		t.Fatalf("statuses = %v", st)
	}
}

func TestWebhookHMACAndRetries(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var calls atomic.Int32
	var mu sync.Mutex
	var got []*http.Request
	var bodies [][]byte
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got, bodies = append(got, r), append(bodies, b)
		mu.Unlock()
		if calls.Add(1) <= 2 {
			http.Error(w, "boom", 500)
			return
		}
		w.WriteHeader(204)
	}))
	defer hook.Close()
	u := e.user("erin", identity.RoleChannelAdmin) // may target private addresses (AllowPrivateNetwork)
	e.putPrefs(u, identity.RoleChannelAdmin, `{"version":0,"webhook":{"enabled":true,"url":"`+hook.URL+`/hook","format":"json"},
		"events":{"wallet.credited":{"email":false,"webhook":true,"inApp":true}}}`)
	if _, err := e.svc.SetWebhookSecret(ctx, e.principal(u, identity.RoleChannelAdmin), "s3cret", RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	e.emit(ev(TypeWalletCredited, "w1", u))
	e.process()
	var next time.Time
	_ = e.pool.QueryRow(ctx, `SELECT next_attempt_at FROM notification_deliveries WHERE user_id = $1`, u).Scan(&next)
	if !next.Equal(e.clk.now().Add(time.Minute)) {
		t.Fatalf("first retry at %v", next)
	}
	e.clk.add(time.Minute)
	e.process()
	_ = e.pool.QueryRow(ctx, `SELECT next_attempt_at FROM notification_deliveries WHERE user_id = $1`, u).Scan(&next)
	if !next.Equal(e.clk.now().Add(5 * time.Minute)) {
		t.Fatalf("second retry at %v", next)
	}
	e.clk.add(4 * time.Minute)
	if e.process(); calls.Load() != 2 {
		t.Fatal("retried before the backoff elapsed")
	}
	e.clk.add(time.Minute)
	e.process()
	if calls.Load() != 3 || e.statuses(u)["event:sent"] != 1 {
		t.Fatalf("calls = %d statuses = %v", calls.Load(), e.statuses(u))
	}
	mu.Lock()
	var p Payload
	sig := got[2].Header.Get(SignatureHeader)
	third := bodies[2]
	mu.Unlock()
	if err := json.Unmarshal(third, &p); err != nil || p.Type != TypeWalletCredited || p.Title != "标题 w1" ||
		p.URL != "https://gw.example.com/console/billing" || p.ID == "" {
		t.Fatalf("payload = %s", third)
	}
	if sig != Sign("s3cret", third) || !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("signature = %q", sig)
	}

	// Test endpoint and other formats.
	for _, f := range []string{"feishu", "dingtalk", "wecom", "slack"} {
		body, _, err := webhookBody(f, p, "", time.Now())
		if err != nil || !json.Valid(body) || !strings.Contains(string(body), "标题 w1") {
			t.Fatalf("%s body = %s", f, body)
		}
	}
	res, err := e.svc.TestWebhook(ctx, u)
	if err != nil || !res.OK || *res.StatusCode != 204 {
		t.Fatalf("test webhook = %+v %v", res, err)
	}
	// Retries stop after 1 + 3 attempts.
	calls.Store(-100)
	e.emit(ev(TypeWalletCredited, "w2", u))
	for i := 0; i < 5; i++ {
		e.process()
		e.clk.add(time.Hour)
	}
	if st := e.statuses(u); st["event:failed"] != 1 {
		t.Fatalf("give up = %v", st)
	}
}

// TestClaimConcurrency runs several workers (several instances on
// PostgreSQL) on one outbox: every delivery is sent exactly once.
func TestClaimConcurrency(t *testing.T) {
	e := newEnv(t)
	var hits sync.Map
	var dup atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		if _, loaded := hits.LoadOrStore(p.ID, true); loaded {
			dup.Add(1)
		}
		w.WriteHeader(200)
	}))
	defer hook.Close()
	const users, events = 4, 30
	for i := 0; i < users; i++ {
		u := e.user("c"+string(rune('a'+i)), identity.RoleChannelAdmin)
		e.putPrefs(u, identity.RoleChannelAdmin, `{"version":0,"email":{"enabled":false},"webhook":{"enabled":true,"url":"`+hook.URL+`"},
			"events":{"wallet.credited":{"email":false,"webhook":true,"inApp":false}}}`)
		for j := 0; j < events; j++ {
			e.emit(ev(TypeWalletCredited, u.String()+string(rune('A'+j)), u))
		}
	}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		svc, err := New(e.svc.opts)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n, err := svc.ProcessDue(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	n := 0
	hits.Range(func(_, _ any) bool { n++; return true })
	var sent int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM notification_deliveries WHERE status = 'sent'`).Scan(&sent)
	if n != users*events || dup.Load() != 0 || sent != users*events {
		t.Fatalf("delivered %d unique, %d duplicates, %d sent rows", n, dup.Load(), sent)
	}
}

func TestPreferencesValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user("frank", identity.RoleUser)
	p, err := e.svc.GetPreferences(ctx, e.principal(u, identity.RoleUser))
	if err != nil || p.Version != 0 || !p.Email.Enabled || !p.Email.Verified || p.Webhook.Format != "json" || p.Digest != "off" ||
		p.Timezone != DefaultTimezone || p.Thresholds.WalletBalanceLow != "1" || !p.SMTPConfigured {
		t.Fatalf("defaults = %+v %v", p, err)
	}
	if _, ok := p.Events[TypePluginPendingApproval]; ok {
		t.Fatal("ineligible event listed")
	}
	if _, ok := p.Events[TypeChannelUnhealthy]; ok {
		t.Fatal("channel events listed for a user without channels")
	}
	if sw := p.Events[TypeModelAdded]; sw.Email || sw.InApp || sw.Webhook {
		t.Fatalf("model.added default = %+v", sw)
	}
	bad := func(body, field string) {
		t.Helper()
		var in PreferencesInput
		_ = json.Unmarshal([]byte(body), &in)
		_, err := e.svc.PutPreferences(ctx, e.principal(u, identity.RoleUser), in, RequestMeta{})
		if err == nil || !strings.Contains(err.Error(), "validation_failed") {
			t.Fatalf("%s: err = %v", body, err)
		}
		var ae interface{ Error() string }
		_ = ae
	}
	bad(`{"version":0,"events":{"plugin.pending_approval":{"email":true,"webhook":false,"inApp":false}}}`, "events.plugin.pending_approval")
	bad(`{"version":0,"events":{"nope":{"email":false,"webhook":false,"inApp":false}}}`, "events.nope")
	bad(`{"version":0,"webhook":{"enabled":true,"url":"http://10.0.0.1/x"}}`, "webhook.url")
	bad(`{"version":0,"webhook":{"enabled":true}}`, "webhook.url")
	bad(`{"version":0,"timezone":"Mars/Base"}`, "timezone")
	bad(`{"version":0,"thresholds":{"walletBalanceLow":"-1"}}`, "thresholds")
	bad(`{"version":0,"email":{"enabled":true,"address":"other@example.com"}}`, "email.address")
	// Explicitly disabled ineligible events and read-only fields are accepted.
	p = e.putPrefs(u, identity.RoleUser, `{"version":0,"smtpConfigured":false,"email":{"enabled":true,"address":null,"verified":false},
		"webhook":{"enabled":false,"url":null,"secretSet":true,"format":"slack"},
		"events":{"plugin.pending_approval":{"email":false,"webhook":false,"inApp":false},"wallet.credited":{"email":true,"webhook":false,"inApp":true}},
		"thresholds":{"walletBalanceLow":"2.50"},"digest":"daily","timezone":"Europe/Berlin"}`)
	if p.Version != 1 || p.Webhook.Format != "slack" || p.Webhook.SecretSet || !p.Events[TypeWalletCredited].Email ||
		p.Thresholds.WalletBalanceLow != "2.5" || !p.Events[TypeSubscriptionExpiring].Email {
		t.Fatalf("saved = %+v", p)
	}
	var in PreferencesInput
	_ = json.Unmarshal([]byte(`{"version":0}`), &in)
	if _, err := e.svc.PutPreferences(ctx, e.principal(u, identity.RoleUser), in, RequestMeta{}); err == nil || !strings.Contains(err.Error(), "version_conflict") {
		t.Fatalf("stale version = %v", err)
	}
	// Admins are eligible for plugin and channel events.
	a := e.user("root", identity.RoleSystemAdmin)
	p = e.putPrefs(a, identity.RoleSystemAdmin, `{"version":0,"events":{"plugin.pending_approval":{"email":false,"webhook":false,"inApp":true}}}`)
	if _, ok := p.Events[TypeChannelAuthFailed]; !ok || p.Events[TypePluginPendingApproval].Email {
		t.Fatalf("admin prefs = %+v", p)
	}
}

func TestNextDigest(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	ny, _ := time.LoadLocation("America/New_York")
	for _, c := range []struct {
		now  time.Time
		loc  *time.Location
		want time.Time
	}{
		{time.Date(2026, 10, 8, 0, 30, 0, 0, time.UTC), sh, time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)},
		{time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC), sh, time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)},
		{time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), ny, time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC), ny, time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)}, // DST ends Nov 1
	} {
		if got := nextDigest(c.now, c.loc); !got.Equal(c.want) {
			t.Errorf("nextDigest(%v, %v) = %v, want %v", c.now, c.loc, got, c.want)
		}
	}
}
