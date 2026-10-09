package app_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"omnigate/internal/app"
	"omnigate/internal/notify/notifytest"
)

// Round 6 (docs/contracts/phase6-api.md): notifications.

// settle waits for queued request logs and background notification emits.
func (e *gwEnv) settle() {
	e.t.Helper()
	if err := e.app.FlushLogs(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.app.Notifications().Wait()
}

// notifs lists c's notifications of the given (comma-separated) types.
func (e *gwEnv) notifs(c *client, types string) []map[string]any {
	e.t.Helper()
	e.settle()
	out := e.mustDo(c, http.MethodGet, "/api/notifications?pageSize=100&type="+url.QueryEscape(types), nil, 200)
	var list []map[string]any
	for _, it := range out["items"].([]any) {
		list = append(list, it.(map[string]any))
	}
	return list
}

func (e *gwEnv) expectNotifs(c *client, typ string, n int) []map[string]any {
	e.t.Helper()
	list := e.notifs(c, typ)
	if len(list) != n {
		e.t.Fatalf("%s: %d notifications, want %d: %v", typ, len(list), n, list)
	}
	return list
}

// useSMTP points the system settings at a fake SMTP server.
func (e *gwEnv) useSMTP(srv *notifytest.Server) map[string]any {
	e.t.Helper()
	st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	return e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"notifications": map[string]any{"smtp": map[string]any{"host": srv.Host, "port": srv.Port, "security": "none",
			"username": "mailer", "password": "smtp-pa55-secret", "from": "OmniGate <noreply@example.com>"}}}}, 200)
}

func (e *gwEnv) prefs(c *client) map[string]any {
	return e.mustDo(c, http.MethodGet, "/api/notifications/preferences", nil, 200)
}

var codeRe = regexp.MustCompile(`验证码：(\d{6})`)

func TestNotificationSettingsSMTPAndEmailVerification(t *testing.T) {
	e := setupGatewayWith(t, app.Options{})
	srv := notifytest.Start(t, "none")

	st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	n := st["settings"].(map[string]any)["notifications"].(map[string]any)
	smtp := n["smtp"].(map[string]any)
	if smtp["host"] != "" || smtp["port"].(float64) != 587 || smtp["security"] != "starttls" || smtp["passwordSet"] != false ||
		n["enabled"] != true || n["emailRateLimitPerHour"].(float64) != 20 || st["readonly"].(map[string]any)["smtpConfigured"] != false ||
		st["sources"].(map[string]any)["notifications.smtp.password"] != "default" {
		t.Fatalf("defaults = %v", st)
	}
	if p := e.prefs(e.carol); p["smtpConfigured"] != false || p["version"].(float64) != 0 {
		t.Fatalf("prefs = %v", p)
	}
	if resp, out := e.carol.do(http.MethodPost, "/api/notifications/email/verify", map[string]any{"address": "c@carol.test"}); resp.StatusCode != 409 ||
		errCode(out) != "smtp_not_configured" {
		t.Fatalf("verify without smtp = %d %v", resp.StatusCode, out)
	}
	if resp, out := e.admin.do(http.MethodPost, "/api/admin/settings/smtp-test", map[string]any{"to": "a@x.test"}); resp.StatusCode != 409 ||
		errCode(out) != "smtp_not_configured" {
		t.Fatalf("smtp-test without smtp = %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.carol.do(http.MethodPost, "/api/admin/settings/smtp-test", map[string]any{"to": "a@x.test"}); resp.StatusCode != 403 {
		t.Fatal("plain user ran smtp-test")
	}
	resp, out := e.admin.do(http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"notifications": map[string]any{"smtp": map[string]any{"security": "ssl", "from": "not an address", "port": 70000}, "emailRateLimitPerHour": 0}}})
	if resp.StatusCode != 422 || len(out["error"].(map[string]any)["details"].(map[string]any)) != 4 {
		t.Fatalf("invalid smtp = %d %v", resp.StatusCode, out)
	}

	st = e.useSMTP(srv)
	smtp = st["settings"].(map[string]any)["notifications"].(map[string]any)["smtp"].(map[string]any)
	if smtp["passwordSet"] != true || smtp["host"] != "127.0.0.1" || smtp["password"] != nil ||
		st["sources"].(map[string]any)["notifications.smtp.host"] != "db" || st["readonly"].(map[string]any)["smtpConfigured"] != true {
		t.Fatalf("smtp saved = %v", st)
	}
	raw, _ := io.ReadAll(strings.NewReader(fmt.Sprint(st)))
	if strings.Contains(string(raw), "smtp-pa55-secret") {
		t.Fatal("password returned")
	}
	audit := e.mustDo(e.admin, http.MethodGet, "/api/admin/audit-logs?action=notifications.smtp_update", nil, 200)
	if items := audit["items"].([]any); len(items) != 1 || strings.Contains(fmt.Sprint(items), "smtp-pa55-secret") {
		t.Fatalf("smtp audit = %v", items)
	}
	upd := e.mustDo(e.admin, http.MethodGet, "/api/admin/audit-logs?action=settings.update", nil, 200)
	if strings.Contains(fmt.Sprint(upd), "smtp-pa55-secret") {
		t.Fatal("password audited")
	}

	res := e.mustDo(e.admin, http.MethodPost, "/api/admin/settings/smtp-test", map[string]any{"to": "ops@example.com"}, 200)
	msgs := srv.Wait(1, 2*time.Second)
	if res["ok"] != true || len(msgs) != 1 || msgs[0].To[0] != "ops@example.com" || !strings.Contains(msgs[0].Subject, "SMTP 测试邮件") ||
		msgs[0].Password != "smtp-pa55-secret" {
		t.Fatalf("smtp-test = %v %+v", res, msgs)
	}
	srv.RejectAuth.Store(true)
	res = e.mustDo(e.admin, http.MethodPost, "/api/admin/settings/smtp-test", map[string]any{"to": "ops@example.com"}, 200)
	if res["ok"] != false || !strings.Contains(res["error"].(string), "身份验证失败") || strings.Contains(res["error"].(string), "smtp-pa55-secret") {
		t.Fatalf("failing smtp-test = %v", res)
	}
	srv.RejectAuth.Store(false)

	// Email verification: code by mail, wrong code, success, expiry, hourly limit.
	if p := e.prefs(e.carol); p["smtpConfigured"] != true || p["email"].(map[string]any)["address"] != nil ||
		p["email"].(map[string]any)["verified"] != true {
		t.Fatalf("prefs = %v", p)
	}
	srv.Reset()
	e.mustDo(e.carol, http.MethodPost, "/api/notifications/email/verify", map[string]any{"address": "c@carol.test"}, 200)
	msgs = srv.Wait(1, 2*time.Second)
	m := codeRe.FindStringSubmatch(msgs[0].Text)
	if len(msgs) != 1 || msgs[0].To[0] != "c@carol.test" || m == nil {
		t.Fatalf("code mail = %+v", msgs)
	}
	wrong := "000000"
	if m[1] == wrong {
		wrong = "111111"
	}
	if resp, out := e.carol.do(http.MethodPost, "/api/notifications/email/confirm", map[string]any{"address": "c@carol.test", "code": wrong}); resp.StatusCode != 422 ||
		errCode(out) != "verification_invalid" {
		t.Fatalf("wrong code = %d %v", resp.StatusCode, out)
	}
	p := e.mustDo(e.carol, http.MethodPost, "/api/notifications/email/confirm", map[string]any{"address": "C@carol.test", "code": m[1]}, 200)
	if p["email"].(map[string]any)["address"] != "c@carol.test" || p["email"].(map[string]any)["verified"] != true || p["version"].(float64) != 1 {
		t.Fatalf("confirmed = %v", p)
	}
	if resp, _ := e.carol.do(http.MethodPost, "/api/notifications/email/confirm", map[string]any{"address": "c@carol.test", "code": m[1]}); resp.StatusCode != 422 {
		t.Fatal("code reused")
	}
	e.mustDo(e.carol, http.MethodPost, "/api/notifications/email/verify", map[string]any{"address": "late@carol.test"}, 200)
	msgs = srv.Wait(2, 2*time.Second)
	late := codeRe.FindStringSubmatch(msgs[1].Text)[1]
	n6 := e.app.Notifications()
	n6.SetClock(func() time.Time { return time.Now().UTC().Add(11 * time.Minute) })
	if resp, out := e.carol.do(http.MethodPost, "/api/notifications/email/confirm", map[string]any{"address": "late@carol.test", "code": late}); resp.StatusCode != 422 ||
		errCode(out) != "verification_expired" {
		t.Fatalf("expired = %d %v", resp.StatusCode, out)
	}
	n6.SetClock(func() time.Time { return time.Now().UTC() })
	for i := 0; i < 3; i++ {
		e.mustDo(e.carol, http.MethodPost, "/api/notifications/email/verify", map[string]any{"address": "x@carol.test"}, 200)
	}
	if resp, out := e.carol.do(http.MethodPost, "/api/notifications/email/verify", map[string]any{"address": "x@carol.test"}); resp.StatusCode != 429 {
		t.Fatalf("6th code = %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.carol.do(http.MethodPost, "/api/notifications/email/verify", map[string]any{"address": "nope"}); resp.StatusCode != 422 {
		t.Fatal("bad address accepted")
	}
	// Custom address can only be the verified one; null switches back to the account email.
	body := map[string]any{"version": 1, "email": map[string]any{"enabled": true, "address": "other@carol.test"}}
	if resp, out := e.carol.do(http.MethodPut, "/api/notifications/preferences", body); resp.StatusCode != 422 ||
		out["error"].(map[string]any)["details"].(map[string]any)["email.address"] == nil {
		t.Fatalf("unverified address = %d %v", resp.StatusCode, out)
	}
	body["email"] = map[string]any{"enabled": true, "address": nil}
	if p := e.mustDo(e.carol, http.MethodPut, "/api/notifications/preferences", body, 200); p["email"].(map[string]any)["address"] != nil {
		t.Fatalf("reset address = %v", p)
	}

	// Password: "" clears the database value (no env fallback), null resets it.
	st = e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	st = e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"notifications": map[string]any{"smtp": map[string]any{"password": ""}}}}, 200)
	if st["settings"].(map[string]any)["notifications"].(map[string]any)["smtp"].(map[string]any)["passwordSet"] != false ||
		st["sources"].(map[string]any)["notifications.smtp.password"] != "db" {
		t.Fatalf("cleared password = %v", st)
	}
	st = e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"notifications": map[string]any{"smtp": map[string]any{"password": nil}, "enabled": false}}}, 200)
	if st["sources"].(map[string]any)["notifications.smtp.password"] != "default" {
		t.Fatalf("reset password = %v", st)
	}
	if p := e.prefs(e.carol); p["smtpConfigured"] != false {
		t.Fatalf("notifications disabled but smtpConfigured = %v", p)
	}
}

func TestNotificationPreferencesAndInbox(t *testing.T) {
	e := setupGatewayWith(t, app.Options{})
	p := e.prefs(e.carol)
	events := p["events"].(map[string]any)
	if p["version"].(float64) != 0 || events["plugin.pending_approval"] != nil || events["channel.unhealthy"] != nil ||
		events["wallet.balance_low"].(map[string]any)["email"] != true || events["wallet.balance_low"].(map[string]any)["webhook"] != false ||
		p["thresholds"].(map[string]any)["walletBalanceLow"] != "1" || p["timezone"] != "Asia/Shanghai" || p["webhook"].(map[string]any)["secretSet"] != false {
		t.Fatalf("defaults = %v", p)
	}
	if ap := e.prefs(e.admin); ap["events"].(map[string]any)["plugin.pending_approval"] == nil || ap["events"].(map[string]any)["channel.unhealthy"] == nil {
		t.Fatalf("admin events = %v", ap["events"])
	}
	// Ineligible event switched on: 422; switched off: accepted.
	resp, out := e.carol.do(http.MethodPut, "/api/notifications/preferences", map[string]any{"version": 0,
		"events": map[string]any{"plugin.pending_approval": map[string]any{"email": true, "webhook": false, "inApp": false}}})
	if resp.StatusCode != 422 || out["error"].(map[string]any)["details"].(map[string]any)["events.plugin.pending_approval"] == nil {
		t.Fatalf("ineligible = %d %v", resp.StatusCode, out)
	}
	// The GET document round-trips (read-only fields included).
	p["events"].(map[string]any)["wallet.credited"] = map[string]any{"email": true, "webhook": false, "inApp": true}
	p["digest"] = "daily"
	saved := e.mustDo(e.carol, http.MethodPut, "/api/notifications/preferences", p, 200)
	if saved["version"].(float64) != 1 || saved["digest"] != "daily" || saved["events"].(map[string]any)["wallet.credited"].(map[string]any)["email"] != true {
		t.Fatalf("saved = %v", saved)
	}
	if resp, _ := e.carol.do(http.MethodPut, "/api/notifications/preferences", p); resp.StatusCode != 409 {
		t.Fatalf("stale version = %d", resp.StatusCode)
	}
	// Webhook secret: write-only, "" clears.
	if out := e.mustDo(e.carol, http.MethodPut, "/api/notifications/webhook/secret", map[string]any{"secret": "hush"}, 200); out["secretSet"] != true {
		t.Fatalf("secret = %v", out)
	}
	if e.prefs(e.carol)["webhook"].(map[string]any)["secretSet"] != true || e.prefs(e.carol)["version"].(float64) != 1 {
		t.Fatal("secretSet not reported (or version bumped)")
	}
	e.mustDo(e.carol, http.MethodPut, "/api/notifications/webhook/secret", map[string]any{"secret": ""}, 200)
	if e.prefs(e.carol)["webhook"].(map[string]any)["secretSet"] != false {
		t.Fatal("secret not cleared")
	}
	if resp, _ := e.carol.do(http.MethodPost, "/api/notifications/webhook/test", nil); resp.StatusCode != 422 {
		t.Fatalf("webhook test without url = %d", resp.StatusCode)
	}

	// Inbox: credits (in-app), filters, read marks.
	carolID := e.userID(e.carol)
	e.credit(carolID, "2")
	e.credit(carolID, "3")
	list := e.expectNotifs(e.carol, "wallet.credited", 2)
	if list[0]["title"] != "钱包入账 3 USD" || list[0]["severity"] != "info" || list[0]["link"] != "/console/billing" || list[0]["readAt"] != nil {
		t.Fatalf("credited = %v", list[0])
	}
	if got := e.notifs(e.carol, "wallet.credited,quota.exhausted"); len(got) != 2 {
		t.Fatalf("multi-type filter = %v", got)
	}
	if got := e.notifs(e.carol, "quota.exhausted"); len(got) != 0 {
		t.Fatalf("type filter = %v", got)
	}
	if c := e.mustDo(e.carol, http.MethodGet, "/api/notifications/unread-count", nil, 200); c["count"].(float64) != 2 {
		t.Fatalf("unread = %v", c)
	}
	if resp, _ := e.carol.do(http.MethodPost, "/api/notifications/read", map[string]any{"ids": []any{list[0]["id"]}}); resp.StatusCode != 204 {
		t.Fatalf("read = %d", resp.StatusCode)
	}
	if un := e.mustDo(e.carol, http.MethodGet, "/api/notifications?unread=true", nil, 200); un["total"].(float64) != 1 {
		t.Fatalf("unread list = %v", un)
	}
	// Other users cannot mark carol's notifications.
	e.mustDo(e.admin, http.MethodPost, "/api/notifications/read", map[string]any{"all": true}, 204)
	if c := e.mustDo(e.carol, http.MethodGet, "/api/notifications/unread-count", nil, 200); c["count"].(float64) != 1 {
		t.Fatalf("unread after admin = %v", c)
	}
	e.mustDo(e.carol, http.MethodPost, "/api/notifications/read", map[string]any{"all": true}, 204)
	if c := e.mustDo(e.carol, http.MethodGet, "/api/notifications/unread-count", nil, 200); c["count"].(float64) != 0 {
		t.Fatalf("unread after all = %v", c)
	}
}
