package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Round 6 (continued), docs/contracts/phase7-api.md §2 and §4.1–4.3, 4.5, 4.9:
// user management.

func (e *gwEnv) user(id string) map[string]any {
	e.t.Helper()
	return e.mustDo(e.admin, http.MethodGet, "/api/admin/users/"+id, nil, 200)
}

func (e *gwEnv) auditCount(action string) (int, []any) {
	e.t.Helper()
	logs := e.mustDo(e.admin, http.MethodGet, "/api/admin/audit-logs?pageSize=100&action="+action, nil, 200)
	items := logs["items"].([]any)
	return len(items), items
}

func TestUserManagement(t *testing.T) {
	e, smtp := eventsEnv(t)
	base := e.h.srv.URL
	ctx := context.Background()
	up := newFakeUpstream(t)
	e.channel(e.admin, map[string]any{"name": "glob", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
	carolID, adminID := e.userID(e.carol), e.userID(e.admin)
	carolKeyID, carolKey := e.key(e.carol, map[string]any{"name": "carol-1"})
	e.key(e.carol, map[string]any{"name": "carol-2"})
	if code, _, raw := readBody(gwPost(t, ctx, base, "/v1/chat/completions", carolKey, chatBody)); code != 200 {
		t.Fatalf("precondition = %d %s", code, raw)
	}
	carol2 := e.h.newClient()
	carol2.login("carol")

	t.Run("detail", func(t *testing.T) {
		e.app.FlushLogs(ctx)
		d := e.user(carolID)
		u := d["user"].(map[string]any)
		if u["id"] != carolID || u["disabledReason"] != nil || u["disabledUntil"] != nil || u["lastLoginAt"] == nil {
			t.Fatalf("user = %v", u)
		}
		ids := d["identities"].([]any)
		if len(ids) != 1 || ids[0].(map[string]any)["provider"] != "github" || ids[0].(map[string]any)["subject"] != "1003" ||
			ids[0].(map[string]any)["createdAt"] == nil {
			t.Fatalf("identities = %v", ids)
		}
		if d["wallet"] != nil {
			t.Fatalf("wallet without ledger = %v", d["wallet"])
		}
		if len(d["keys"].([]any)) != 2 || d["keys"].([]any)[0].(map[string]any)["prefix"] == nil {
			t.Fatalf("keys = %v", d["keys"])
		}
		if d["sessions"].(map[string]any)["active"] != float64(2) || d["channels"].(map[string]any)["own"] != float64(0) {
			t.Fatalf("sessions / channels = %v %v", d["sessions"], d["channels"])
		}
		usage := d["usage30d"].(map[string]any)
		if usage["requests"] != float64(1) || usage["tokens"] != float64(12) || usage["charge"] != "0" {
			t.Fatalf("usage30d = %v", usage)
		}
		if subs := d["subscriptions"].([]any); len(subs) != 0 {
			t.Fatalf("subscriptions = %v", subs)
		}
		e.credit(carolID, "5")
		if w := e.user(carolID)["wallet"].(map[string]any); w["balance"] != "5" || w["reserved"] != "0" {
			t.Fatalf("wallet = %v", w)
		}
		if a := e.user(adminID); a["channels"].(map[string]any)["own"] != float64(1) {
			t.Fatalf("admin channels = %v", a["channels"])
		}
		e.mustDo(e.admin, http.MethodGet, "/api/admin/users/"+uuid.NewString(), nil, 404)
		e.mustDo(e.carol, http.MethodGet, "/api/admin/users/"+adminID, nil, 403)
	})

	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	t.Run("disable with reason and until", func(t *testing.T) {
		v := e.user(carolID)["user"].(map[string]any)["version"]
		// Reason required; until must be in the future.
		resp, out := e.admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"status": "disabled", "version": v})
		if resp.StatusCode != 422 || out["error"].(map[string]any)["details"].(map[string]any)["disabledReason"] == nil {
			t.Fatalf("missing reason = %d %v", resp.StatusCode, out)
		}
		resp, out = e.admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"status": "disabled", "disabledReason": "x",
			"disabledUntil": time.Now().Add(-time.Minute), "version": v})
		if resp.StatusCode != 422 || out["error"].(map[string]any)["details"].(map[string]any)["disabledUntil"] == nil {
			t.Fatalf("past until = %d %v", resp.StatusCode, out)
		}
		resp, out = e.admin.do(http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"status": "disabled", "disabledReason": strings.Repeat("长", 201), "version": v})
		if resp.StatusCode != 422 {
			t.Fatalf("long reason = %d %v", resp.StatusCode, out)
		}
		u := e.mustDo(e.admin, http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"status": "disabled",
			"disabledReason": "spam & abuse", "disabledUntil": until.Format(time.RFC3339), "version": v}, 200)
		if u["status"] != "disabled" || u["disabledReason"] != "spam & abuse" || u["disabledUntil"] != until.Format(time.RFC3339) {
			t.Fatalf("patched = %v", u)
		}
		// Sessions are revoked; the list shows reason and until.
		if resp, _ := e.carol.do(http.MethodGet, "/api/me", nil); resp.StatusCode != 401 {
			t.Fatalf("session after disable = %d", resp.StatusCode)
		}
		if n := e.user(carolID)["sessions"].(map[string]any)["active"]; n != float64(0) {
			t.Fatalf("active sessions = %v", n)
		}
		list := e.mustDo(e.admin, http.MethodGet, "/api/admin/users?q=carol", nil, 200)
		lu := list["items"].([]any)[0].(map[string]any)
		if lu["disabledReason"] != "spam & abuse" || lu["disabledUntil"] == nil {
			t.Fatalf("list item = %v", lu)
		}
		// Login redirect carries reason and until.
		loc := e.h.newClient().login("carol")
		lu2, _ := url.Parse(loc)
		q := lu2.Query()
		if lu2.Path != "/login" || q.Get("error") != "account_disabled" || q.Get("reason") != "spam & abuse" || q.Get("until") != until.Format(time.RFC3339) ||
			strings.Contains(loc, "+") {
			t.Fatalf("login redirect = %q", loc)
		}
		// The gateway answers 403 account_disabled with the reason.
		code, body, raw := readBody(gwPost(t, ctx, base, "/v1/chat/completions", carolKey, chatBody))
		if code != 403 || body["error"].(map[string]any)["code"] != "account_disabled" || !strings.Contains(body["error"].(map[string]any)["message"].(string), "spam & abuse") {
			t.Fatalf("gateway = %d %s", code, raw)
		}
		// A disabled user still gets the account email (in-app copy shows after re-enable).
		msgs := smtp.Wait(1, 3*time.Second)
		found := false
		for _, m := range msgs {
			if m.To[0] == "carol@example.com" && strings.Contains(m.Subject, "停用") {
				found = true
			}
		}
		if !found {
			t.Fatalf("no account email to carol (%d messages)", len(msgs))
		}
		if n, items := e.auditCount("user.update"); n == 0 || !strings.Contains(stringify(items[0]), "spam & abuse") {
			t.Fatalf("audit user.update = %v", items)
		}
	})

	t.Run("enable clears reason and until", func(t *testing.T) {
		v := e.user(carolID)["user"].(map[string]any)["version"]
		u := e.mustDo(e.admin, http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"status": "active", "version": v}, 200)
		if u["status"] != "active" || u["disabledReason"] != nil || u["disabledUntil"] != nil {
			t.Fatalf("enabled = %v", u)
		}
		if code, _, raw := readBody(gwPost(t, ctx, base, "/v1/chat/completions", carolKey, chatBody)); code != 200 {
			t.Fatalf("key after enable = %d %s", code, raw)
		}
		// Revoked sessions stay revoked.
		if resp, _ := carol2.do(http.MethodGet, "/api/me", nil); resp.StatusCode != 401 {
			t.Fatal("revoked session came back")
		}
		e.carol.login("carol")
		n := e.notifs(e.carol, "account.status_changed")
		if len(n) != 2 || n[0]["title"] != "你的账号已恢复" || !strings.Contains(n[1]["body"].(string), "spam & abuse") {
			t.Fatalf("account notifications = %v", n)
		}
		// The in-app switch of account.status_changed cannot be turned off.
		p := e.prefs(e.carol)
		p["events"].(map[string]any)["account.status_changed"] = map[string]any{"email": false, "webhook": false, "inApp": false}
		saved := e.mustDo(e.carol, http.MethodPut, "/api/notifications/preferences", p, 200)
		if sw := saved["events"].(map[string]any)["account.status_changed"].(map[string]any); sw["inApp"] != true || sw["email"] != false {
			t.Fatalf("account switches = %v", sw)
		}
	})

	t.Run("auto-enable after until", func(t *testing.T) {
		v := e.user(carolID)["user"].(map[string]any)["version"]
		soon := time.Now().Add(1500 * time.Millisecond)
		e.mustDo(e.admin, http.MethodPatch, "/api/admin/users/"+carolID, map[string]any{"status": "disabled", "disabledReason": "cool down",
			"disabledUntil": soon, "version": v}, 200)
		if code, _, _ := readBody(gwPost(t, ctx, base, "/v1/chat/completions", carolKey, chatBody)); code != 403 {
			t.Fatalf("suspended key = %d", code)
		}
		if n, _ := e.app.EnableDueUsers(ctx); n != 0 {
			t.Fatalf("enabled before until: %d", n)
		}
		time.Sleep(time.Until(soon) + 100*time.Millisecond)
		if n, err := e.app.EnableDueUsers(ctx); n != 1 || err != nil {
			t.Fatalf("auto-enable = %d %v", n, err)
		}
		u := e.user(carolID)["user"].(map[string]any)
		if u["status"] != "active" || u["disabledReason"] != nil {
			t.Fatalf("after auto-enable = %v", u)
		}
		if n, items := e.auditCount("user.auto_enable"); n != 1 || items[0].(map[string]any)["actorId"] != nil {
			t.Fatalf("audit auto_enable = %v", items)
		}
		if code, _, _ := readBody(gwPost(t, ctx, base, "/v1/chat/completions", carolKey, chatBody)); code != 200 {
			t.Fatalf("key after auto-enable = %d", code)
		}
		e.carol.login("carol")
		n := e.notifs(e.carol, "account.status_changed")
		if len(n) != 4 || !strings.Contains(n[0]["body"].(string), "自动恢复") {
			t.Fatalf("account notifications = %v", n)
		}
	})

	t.Run("logout and keys disable", func(t *testing.T) {
		other := e.h.newClient()
		other.login("carol")
		out := e.mustDo(e.admin, http.MethodPost, "/api/admin/users/"+carolID+"/logout", nil, 200)
		if out["revoked"] != float64(2) {
			t.Fatalf("logout = %v", out)
		}
		if resp, _ := other.do(http.MethodGet, "/api/me", nil); resp.StatusCode != 401 {
			t.Fatal("session survived forced logout")
		}
		resp, out := e.admin.do(http.MethodPost, "/api/admin/users/"+adminID+"/logout", nil)
		if resp.StatusCode != 409 || out["error"].(map[string]any)["code"] != "cannot_disable_self" {
			t.Fatalf("self logout = %d %v", resp.StatusCode, out)
		}
		e.mustDo(e.admin, http.MethodPost, "/api/admin/users/"+uuid.NewString()+"/logout", nil, 404)
		if n, _ := e.auditCount("user.logout"); n != 1 {
			t.Fatalf("audit user.logout = %d", n)
		}

		out = e.mustDo(e.admin, http.MethodPost, "/api/admin/users/"+carolID+"/keys/disable", nil, 200)
		if out["disabled"] != float64(2) {
			t.Fatalf("keys disable = %v", out)
		}
		if code, _, _ := readBody(gwPost(t, ctx, base, "/v1/chat/completions", carolKey, chatBody)); code != 401 {
			t.Fatalf("disabled key = %d", code)
		}
		if n, _ := e.auditCount("user.keys_disable"); n != 1 {
			t.Fatalf("audit keys_disable = %d", n)
		}
		// The user can enable a key again.
		e.carol.login("carol")
		keys := e.mustDo(e.carol, http.MethodGet, "/api/keys", nil, 200)
		var ver any
		for _, k := range keys["items"].([]any) {
			if km := k.(map[string]any); km["id"] == carolKeyID {
				ver = km["version"]
			}
		}
		e.mustDo(e.carol, http.MethodPatch, "/api/keys/"+carolKeyID, map[string]any{"status": "enabled", "version": ver}, 200)
		if code, _, _ := readBody(gwPost(t, ctx, base, "/v1/chat/completions", carolKey, chatBody)); code != 200 {
			t.Fatalf("re-enabled key = %d", code)
		}
		if n := e.notifs(e.carol, "account.status_changed"); len(n) != 5 || n[0]["title"] != "你已被强制下线" {
			t.Fatalf("logout notification = %v", n)
		}
	})

	t.Run("batch", func(t *testing.T) {
		bobID := e.userID(e.ops)
		missing := uuid.NewString()
		resp, out := e.admin.do(http.MethodPost, "/api/admin/users/batch", map[string]any{"ids": []string{carolID}, "action": "disable"})
		if resp.StatusCode != 422 || out["error"].(map[string]any)["details"].(map[string]any)["reason"] == nil {
			t.Fatalf("batch without reason = %d %v", resp.StatusCode, out)
		}
		e.mustDo(e.admin, http.MethodPost, "/api/admin/users/batch", map[string]any{"ids": []string{}, "action": "enable"}, 422)
		e.mustDo(e.admin, http.MethodPost, "/api/admin/users/batch", map[string]any{"ids": []string{carolID}, "action": "nuke"}, 422)
		res := e.mustDo(e.admin, http.MethodPost, "/api/admin/users/batch", map[string]any{
			"ids": []string{carolID, bobID, adminID, missing, "not-a-uuid"}, "action": "disable", "reason": "batch", "until": nil}, 200)
		if s := stringify(res["succeeded"]); s != stringify([]any{carolID, bobID}) {
			t.Fatalf("succeeded = %v", res)
		}
		codes := map[string]string{}
		for _, f := range res["failed"].([]any) {
			fm := f.(map[string]any)
			codes[fm["id"].(string)] = fm["code"].(string)
			if fm["message"] == "" {
				t.Fatalf("failure without message: %v", fm)
			}
		}
		if codes[adminID] != "cannot_disable_self" || codes[missing] != "not_found" || codes["not-a-uuid"] != "not_found" || len(codes) != 3 {
			t.Fatalf("failed = %v", res["failed"])
		}
		if u := e.user(bobID)["user"].(map[string]any); u["status"] != "disabled" || u["disabledReason"] != "batch" || u["disabledUntil"] != nil {
			t.Fatalf("bob = %v", u)
		}
		if n, _ := e.auditCount("user.batch"); n != 1 {
			t.Fatalf("audit user.batch = %d", n)
		}
		// Enable both again (one is a no-op for an active user).
		res = e.mustDo(e.admin, http.MethodPost, "/api/admin/users/batch", map[string]any{"ids": []string{carolID, bobID, adminID}, "action": "enable"}, 200)
		if len(res["succeeded"].([]any)) != 3 || len(res["failed"].([]any)) != 0 {
			t.Fatalf("batch enable = %v", res)
		}
		// last_admin: alice's own row is disabled with an ended suspension (she is
		// still signed in until the auto-enable job runs), bob is the only other
		// system admin.
		v := e.user(bobID)["user"].(map[string]any)["version"]
		e.mustDo(e.admin, http.MethodPatch, "/api/admin/users/"+bobID, map[string]any{"role": "system_admin", "version": v}, 200)
		if _, err := e.app.DB().Exec(ctx, `UPDATE users SET status = 'disabled', disabled_reason = 'x', disabled_until = $2 WHERE id = $1`,
			uuid.MustParse(adminID), time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		res = e.mustDo(e.admin, http.MethodPost, "/api/admin/users/batch", map[string]any{"ids": []string{bobID}, "action": "disable", "reason": "r"}, 200)
		if f := res["failed"].([]any); len(f) != 1 || f[0].(map[string]any)["code"] != "last_admin" {
			t.Fatalf("last admin = %v", res)
		}
		if n, _ := e.app.EnableDueUsers(ctx); n != 1 {
			t.Fatalf("alice auto-enable = %d", n)
		}
		// Batch logout (self is refused).
		res = e.mustDo(e.admin, http.MethodPost, "/api/admin/users/batch", map[string]any{"ids": []string{carolID, adminID}, "action": "logout"}, 200)
		if len(res["succeeded"].([]any)) != 1 || res["failed"].([]any)[0].(map[string]any)["code"] != "cannot_disable_self" {
			t.Fatalf("batch logout = %v", res)
		}
		if resp, _ := e.carol.do(http.MethodGet, "/api/me", nil); resp.StatusCode != 401 {
			t.Fatal("batch logout left carol signed in")
		}
	})
}

func stringify(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}
