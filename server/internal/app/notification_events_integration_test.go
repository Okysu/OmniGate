package app_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"omnigate/internal/app"
	"omnigate/internal/notify/notifytest"
)

// eventsEnv: alice (system_admin), bob (channel_admin), carol (user), a fake
// SMTP server and loopback upstreams.
func eventsEnv(t *testing.T) (*gwEnv, *notifytest.Server) {
	e := setupGatewayWith(t, app.Options{UpstreamTransport: loopback})
	srv := notifytest.Start(t, "none")
	e.useSMTP(srv)
	e.platformChannel(map[string]any{"name": "bob-ch", "type": "openai", "baseUrl": "https://bob.example.com/v1", "models": models("bx")})
	return e, srv
}

func (e *gwEnv) scanModels() {
	e.t.Helper()
	time.Sleep(80 * time.Millisecond) // registry reloads asynchronously
	if err := e.app.Notifications().ScanModels(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *gwEnv) scanExpiry() {
	e.t.Helper()
	if err := e.app.Notifications().ScanExpiry(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func TestNotificationEventsWalletPlansKeys(t *testing.T) {
	e, srv := eventsEnv(t)
	carolID := e.userID(e.carol)

	// wallet.balance_low: crossing from above the threshold, once until re-armed.
	e.credit(carolID, "1.5")
	e.credit(carolID, "-0.6") // 0.9 < 1
	low := e.expectNotifs(e.carol, "wallet.balance_low", 1)
	if low[0]["severity"] != "warning" || low[0]["data"].(map[string]any)["available"] != "0.9" {
		t.Fatalf("balance low = %v", low)
	}
	e.credit(carolID, "-0.1")
	e.expectNotifs(e.carol, "wallet.balance_low", 1)
	e.credit(carolID, "1") // 1.8: re-armed
	e.credit(carolID, "-1")
	e.expectNotifs(e.carol, "wallet.balance_low", 2)
	e.expectNotifs(e.carol, "wallet.credited", 2) // positive adjustments only
	e.expectNotifs(e.admin, "wallet.balance_low,wallet.credited", 0)
	msgs := srv.Wait(2, 3*time.Second)
	n := 0
	for _, m := range msgs {
		if m.To[0] == "carol@example.com" && strings.Contains(m.Subject, "钱包余额不足") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("balance low emails = %d (%d messages)", n, len(msgs))
	}
	// One-click unsubscribe from the email (public, idempotent).
	link, _ := url.Parse(strings.Trim(msgs[0].Header.Get("List-Unsubscribe"), "<>"))
	anon := e.h.newClient()
	for i := 0; i < 2; i++ {
		resp, _ := anon.do(http.MethodGet, "/api/notifications/unsubscribe?"+link.RawQuery, nil)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("unsubscribe = %d", resp.StatusCode)
		}
	}
	if sw := e.prefs(e.carol)["events"].(map[string]any)["wallet.balance_low"].(map[string]any); sw["email"] != false || sw["inApp"] != true {
		t.Fatalf("after unsubscribe = %v", sw)
	}
	if resp, _ := anon.do(http.MethodGet, "/api/notifications/unsubscribe?token=v1:x:y", nil); resp.StatusCode != 400 {
		t.Fatalf("bad token = %d", resp.StatusCode)
	}

	// subscription.expiring / expired (cancel), once each.
	plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
		"name": "Short", "description": "", "duration": "2d", "models": []string{"m1"}, "stackable": true,
		"rules": []map[string]any{{"id": "r", "label": "次数", "meter": "requests", "window": map[string]any{"kind": "period", "every": "1d"}, "limit": "5"}},
	}, 201)
	sub := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": carolID, "planId": plan["id"], "periods": 1}, 201)
	e.scanExpiry()
	e.scanExpiry()
	exp := e.expectNotifs(e.carol, "subscription.expiring", 1)
	if !strings.Contains(exp[0]["title"].(string), "Short") {
		t.Fatalf("expiring = %v", exp)
	}
	subID := sub["id"]
	if subID == nil {
		t.Fatalf("grant = %v", sub)
	}

	// quota.near_limit at 80 %, quota.exhausted at 100 % — once per window.
	up := newModelUpstream(t, 0)
	e.channel(e.admin, map[string]any{"name": "plat", "type": "openai", "baseUrl": pub(up.srv.URL) + "/v1", "scope": "global", "models": models("m1")})
	_, key := e.key(e.carol, map[string]any{"name": "ck"})
	for i := 1; i <= 6; i++ {
		code, _, raw := readBody(gwPost(t, context.Background(), e.h.srv.URL, "/v1/chat/completions", key, chat("m1")))
		if i <= 5 && code != 200 {
			t.Fatalf("call %d = %d %s", i, code, raw)
		}
		e.settle()
		near, exhausted := len(e.notifs(e.carol, "quota.near_limit")), len(e.notifs(e.carol, "quota.exhausted"))
		wantNear, wantEx := 0, 0
		if i >= 4 {
			wantNear = 1
		}
		if i >= 5 {
			wantEx = 1
		}
		if near != wantNear || exhausted != wantEx {
			t.Fatalf("after call %d: near %d exhausted %d", i, near, exhausted)
		}
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions/"+subID.(string)+"/cancel", map[string]any{"note": "bye"}, 200)
	e.scanExpiry()
	e.scanExpiry()
	if ended := e.expectNotifs(e.carol, "subscription.expired", 1); ended[0]["data"].(map[string]any)["cancelled"] != true {
		t.Fatalf("expired = %v", ended)
	}

	// key.expiring: 7 days ahead, once per expiry date.
	e.key(e.carol, map[string]any{"name": "soon", "expiresAt": time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)})
	e.key(e.carol, map[string]any{"name": "later", "expiresAt": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	e.scanExpiry()
	e.scanExpiry()
	if k := e.expectNotifs(e.carol, "key.expiring", 1); !strings.Contains(k[0]["title"].(string), "soon") || k[0]["link"] != "/console/keys" {
		t.Fatalf("key expiring = %v", k)
	}
}

func TestNotificationEventsChannelsModelsPlugins(t *testing.T) {
	e, _ := eventsEnv(t)
	carolID := e.userID(e.carol)
	_ = carolID
	plat, own := newModelUpstream(t, 0), newModelUpstream(t, 0)
	platID := e.channel(e.admin, map[string]any{"name": "plat", "type": "openai", "baseUrl": pub(plat.srv.URL) + "/v1", "scope": "global", "models": models("m1", "m2")})
	ownID := e.channel(e.carol, map[string]any{"name": "carol-own", "type": "openai", "baseUrl": pub(own.srv.URL) + "/v1", "models": models("c1")})
	_, key := e.key(e.carol, map[string]any{"name": "ck"})
	call := func(model string) int {
		code, _, _ := readBody(gwPost(t, context.Background(), e.h.srv.URL, "/v1/chat/completions", key, chat(model)))
		return code
	}

	// channel.unhealthy / recovered: platform channel → owner and channels.manage admins, never users it is shared with.
	plat.status.Store(500)
	for i := 0; i < 6; i++ {
		e.mustDo(e.admin, http.MethodPost, "/api/channels/"+platID+"/test", nil, 200)
	}
	un := e.expectNotifs(e.admin, "channel.unhealthy", 1)
	if un[0]["severity"] != "critical" || un[0]["link"] != "/console/channels/"+platID || un[0]["data"].(map[string]any)["channelId"] != platID {
		t.Fatalf("unhealthy = %v", un)
	}
	e.expectNotifs(e.ops, "channel.unhealthy", 1)
	e.expectNotifs(e.carol, "channel.unhealthy", 0)
	plat.status.Store(0)
	e.mustDo(e.admin, http.MethodPost, "/api/channels/"+platID+"/test", nil, 200)
	e.mustDo(e.admin, http.MethodPost, "/api/channels/"+platID+"/test", nil, 200)
	e.expectNotifs(e.admin, "channel.recovered", 1)
	e.expectNotifs(e.ops, "channel.recovered", 1)

	// channel.auth_failed: a regular user's channel → only its owner; at most once per 6 hours.
	own.status.Store(401)
	for i := 0; i < 4; i++ {
		if code := call("c1"); code < 400 {
			t.Fatalf("401 upstream answered %d", code)
		}
	}
	e.mustDo(e.carol, http.MethodPost, "/api/channels/"+ownID+"/test", nil, 200)
	af := e.expectNotifs(e.carol, "channel.auth_failed", 1)
	if af[0]["data"].(map[string]any)["statusCode"].(float64) != 401 {
		t.Fatalf("auth failed = %v", af)
	}
	e.expectNotifs(e.admin, "channel.auth_failed", 0)
	own.status.Store(0)

	// Alerts summary: only manageable channels; recent alert-class notifications.
	cs := e.mustDo(e.carol, http.MethodGet, "/api/alerts/summary", nil, 200)
	ch := cs["channels"].(map[string]any)
	// carol's channel failed five times within a minute (401 counts against health): unhealthy + auth_failed.
	if ch["total"].(float64) != 1 || ch["down"].(float64) != 1 || len(cs["balances"].([]any)) != 0 || len(cs["recent"].([]any)) != 2 ||
		!strings.Contains(fmt.Sprint(cs["recent"]), "channel.auth_failed") {
		t.Fatalf("carol summary = %v", cs)
	}
	as := e.mustDo(e.admin, http.MethodGet, "/api/alerts/summary", nil, 200)
	if as["channels"].(map[string]any)["total"].(float64) != 3 || len(as["recent"].([]any)) != 2 {
		t.Fatalf("admin summary = %v", as)
	}

	// model.price_changed: only users who called the model on the platform in the last 30 days.
	if code := call("m1"); code != 200 {
		t.Fatalf("m1 = %d", code)
	}
	e.settle()
	eff := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m1", "inputPerM": "2", "outputPerM": "4", "effectiveAt": eff}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m2", "inputPerM": "2", "outputPerM": "4"}, 201)
	pc := e.expectNotifs(e.carol, "model.price_changed", 1)
	if !strings.Contains(pc[0]["body"].(string), "将于") || pc[0]["data"].(map[string]any)["model"] != "m1" {
		t.Fatalf("price changed = %v", pc)
	}
	e.expectNotifs(e.admin, "model.price_changed", 0)
	e.expectNotifs(e.ops, "model.price_changed", 0)

	// model.added (opt-in) and model.removed.
	e.scanModels()
	ap := e.prefs(e.admin)
	ap["events"].(map[string]any)["model.added"] = map[string]any{"email": false, "webhook": false, "inApp": true}
	e.mustDo(e.admin, http.MethodPut, "/api/notifications/preferences", ap, 200)
	e.channel(e.admin, map[string]any{"name": "plat-new", "type": "openai", "baseUrl": pub(plat.srv.URL) + "/v1", "scope": "global", "models": models("m9")})
	e.scanModels()
	e.scanModels()
	if added := e.expectNotifs(e.admin, "model.added", 1); added[0]["data"].(map[string]any)["model"] != "m9" {
		t.Fatalf("added = %v", added)
	}
	e.expectNotifs(e.carol, "model.added", 0) // off by default
	c := e.mustDo(e.admin, http.MethodGet, "/api/channels/"+platID, nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+platID, map[string]any{"status": "disabled", "version": c["version"]}, 200)
	e.scanModels()
	e.scanModels()
	rm := e.expectNotifs(e.carol, "model.removed", 1)
	if rm[0]["data"].(map[string]any)["model"] != "m1" {
		t.Fatalf("removed = %v", rm)
	}
	e.expectNotifs(e.admin, "model.removed", 0)

	// plugin.pending_approval: plugins.trust only (alice), not channel_admin bob.
	pl := e.mustDo(e.admin, http.MethodPost, "/api/plugins", map[string]any{"id": "acme.signer", "name": "签名", "template": "blank"}, 201)
	pid := pl["id"].(string)
	e.mustDo(e.admin, http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": signerFiles("0.1.0", ""), "version": 1}, 200)
	v1 := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/publish", nil, 201)
	if v1["approval"] != "pending" {
		t.Fatalf("v1 = %v", v1)
	}
	pp := e.expectNotifs(e.admin, "plugin.pending_approval", 1)
	if pp[0]["link"] != "/console/plugins/"+pid {
		t.Fatalf("pending = %v", pp)
	}
	e.expectNotifs(e.ops, "plugin.pending_approval", 0)
	e.expectNotifs(e.carol, "plugin.pending_approval", 0)
}

func TestNotificationUpstreamBalanceAndWebhook(t *testing.T) {
	e, _ := eventsEnv(t)
	up := newPluginUpstream(t)
	list := e.mustDo(e.admin, http.MethodGet, "/api/plugins", nil, 200)
	dsVersion := pluginByKey(t, list, "community.deepseek")["latest"].(map[string]any)["id"].(string)
	if resp, out := e.admin.do(http.MethodPost, "/api/channels", map[string]any{"name": "ds", "pluginVersionId": dsVersion,
		"baseUrl": up.srv.URL + "/v1", "apiKey": "sk-deepseek-123456", "alerts": map[string]any{"balanceBelow": "abc"}}); resp.StatusCode != 422 {
		t.Fatalf("bad threshold = %d %v", resp.StatusCode, out)
	}
	ch := e.mustDo(e.admin, http.MethodPost, "/api/channels", map[string]any{"name": "ds", "pluginVersionId": dsVersion,
		"baseUrl": up.srv.URL + "/v1", "apiKey": "sk-deepseek-123456", "alerts": map[string]any{"balanceBelow": "100.0"}}, 201)
	chID := ch["id"].(string)
	if ch["alerts"].(map[string]any)["balanceBelow"] != "100" {
		t.Fatalf("alerts = %v", ch["alerts"])
	}

	// Webhook for alice with HMAC (loopback is allowed for channel managers here).
	hook := newWebhookSink(t)
	ap := e.prefs(e.admin)
	ap["webhook"] = map[string]any{"enabled": true, "url": hook.srv.URL + "/hook", "format": "json"}
	ap["events"].(map[string]any)["upstream.balance_low"] = map[string]any{"email": true, "webhook": true, "inApp": true}
	e.mustDo(e.admin, http.MethodPut, "/api/notifications/preferences", ap, 200)
	e.mustDo(e.admin, http.MethodPut, "/api/notifications/webhook/secret", map[string]any{"secret": "whsec"}, 200)
	if res := e.mustDo(e.admin, http.MethodPost, "/api/notifications/webhook/test", nil, 200); res["ok"] != true || res["statusCode"].(float64) != 200 {
		t.Fatalf("webhook test = %v", res)
	}

	invoke := func() {
		e.mustDo(e.admin, http.MethodPost, "/api/channels/"+chID+"/capabilities/balance.get", nil, 200)
	}
	invoke()
	invoke()
	bl := e.expectNotifs(e.admin, "upstream.balance_low", 1)
	d := bl[0]["data"].(map[string]any)
	if d["total"] != "88.80" || d["currency"] != "CNY" || d["threshold"] != "100" {
		t.Fatalf("balance low = %v", bl)
	}
	e.expectNotifs(e.ops, "upstream.balance_low", 1) // platform channel: channels.manage admins too
	e.expectNotifs(e.carol, "upstream.balance_low", 0)
	sum := e.mustDo(e.admin, http.MethodGet, "/api/alerts/summary", nil, 200)
	bal := sum["balances"].([]any)
	if len(bal) != 1 || bal[0].(map[string]any)["low"] != true || bal[0].(map[string]any)["currency"] != "CNY" ||
		bal[0].(map[string]any)["threshold"] != "100" || bal[0].(map[string]any)["channelName"] != "ds" {
		t.Fatalf("balances = %v", bal)
	}
	if len(e.mustDo(e.ops, http.MethodGet, "/api/alerts/summary", nil, 200)["balances"].([]any)) != 1 {
		t.Fatal("channels.manage admin does not see platform balances")
	}
	// Re-arm: above the threshold, then below again.
	set := func(v any) {
		c := e.mustDo(e.admin, http.MethodGet, "/api/channels/"+chID, nil, 200)
		e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+chID, map[string]any{"alerts": map[string]any{"balanceBelow": v}, "version": c["version"]}, 200)
	}
	set("50")
	invoke()
	set("100")
	invoke()
	e.expectNotifs(e.admin, "upstream.balance_low", 2)
	set(nil)
	invoke()
	if s := e.mustDo(e.admin, http.MethodGet, "/api/alerts/summary", nil, 200)["balances"].([]any)[0].(map[string]any); s["low"] != false || s["threshold"] != nil {
		t.Fatalf("no threshold = %v", s)
	}

	// The webhook received the two alerts, signed.
	got := hook.wait(t, 3) // test message + 2 alerts
	for _, r := range got[1:] {
		if r.header.Get("X-OmniGate-Signature") != signHMAC("whsec", r.body) || !strings.Contains(string(r.body), `"type":"upstream.balance_low"`) {
			t.Fatalf("webhook request = %s %v", r.body, r.header)
		}
	}
}
