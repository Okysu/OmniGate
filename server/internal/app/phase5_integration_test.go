package app_test

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"omnigate/internal/app"
)

// Phase 5 (docs/contracts/phase5-api.md): channel tiers, model info, plaza.

// loopback dials 127.0.0.1 whatever the host name: channels use public-looking
// host names (pub) that regular users may configure, served by local fakes.
var loopback = &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
}}

// pub rewrites a httptest URL to a public-looking host name.
func pub(u string) string { return strings.Replace(u, "127.0.0.1", "upstream.example.com", 1) }

// setupTiers is a gateway whose channels may all reach loopback upstreams
// (regular users' channels included) with a second regular user, bob.
func setupTiers(t *testing.T) (*gwEnv, *client) {
	e := setupGatewayWith(t, app.Options{UpstreamTransport: loopback})
	bob := e.h.newClient()
	bob.login("bob")
	return e, bob
}

func (e *gwEnv) enforceBilling() {
	e.t.Helper()
	st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"billing": map[string]any{"enforce": true}}}, 200)
}

func (e *gwEnv) credit(userID, amount string) {
	e.t.Helper()
	w := e.mustDo(e.admin, http.MethodGet, "/api/admin/billing/wallets/"+userID, nil, 200)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/wallets/"+userID+"/adjust", map[string]any{"amount": amount, "note": "test", "version": w["version"]}, 200)
}

// lastLog returns the newest request log entry visible to c (after a flush).
func (e *gwEnv) lastLog(c *client, query string) map[string]any {
	e.t.Helper()
	if err := e.app.FlushLogs(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	logs := e.mustDo(c, http.MethodGet, "/api/logs?pageSize=1&"+query, nil, 200)
	items := logs["items"].([]any)
	if len(items) == 0 {
		e.t.Fatalf("no log for %s", query)
	}
	return items[0].(map[string]any)
}

func TestChannelTiersRoutingAndBilling(t *testing.T) {
	e, bob := setupTiers(t)
	base := e.h.srv.URL
	carolID, bobID := e.userID(e.carol), e.userID(bob)
	own, plat, plat2, shared, bad := newModelUpstream(t, 0), newModelUpstream(t, 0), newModelUpstream(t, 0), newModelUpstream(t, 0), newModelUpstream(t, 500)

	// Platform: alice's global channels. Own: carol's private channel.
	// Shared: bob (a regular user) shares his channel with carol.
	pID := e.channel(e.admin, map[string]any{"name": "plat", "type": "openai", "baseUrl": pub(plat.srv.URL) + "/v1", "scope": "global", "priority": 100, "models": models("m1", "m2")})
	p2ID := e.channel(e.admin, map[string]any{"name": "plat2", "type": "openai", "baseUrl": pub(plat2.srv.URL) + "/v1", "scope": "global", "models": models("m1")})
	e.channel(e.admin, map[string]any{"name": "plat-bad", "type": "openai", "baseUrl": pub(bad.srv.URL) + "/v1", "scope": "global", "models": models("m3")})
	oID := e.channel(e.carol, map[string]any{"name": "carol-own", "type": "openai", "baseUrl": pub(own.srv.URL) + "/v1", "priority": -5, "models": models("m1")})
	sID := e.channel(bob, map[string]any{"name": "bob-shared", "type": "openai", "baseUrl": pub(shared.srv.URL) + "/v1", "scope": "shared",
		"sharedWith": []string{carolID}, "priority": -10, "models": models("m2")})
	e.acceptShare(e.carol, sID)
	for _, m := range []string{"m1", "m2", "m3"} {
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": m, "inputPerM": "1000", "outputPerM": "1000"}, 201)
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "cost", "model": "up-m1", "channelId": oID, "inputPerM": "1", "outputPerM": "1"}, 201)
	e.enforceBilling()
	_, carolKey := e.key(e.carol, map[string]any{"name": "ck"})
	call := func(key, model string) (int, map[string]any, string) {
		return readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat(model)))
	}
	wallet := func(c *client) map[string]any { return e.mustDo(c, http.MethodGet, "/api/billing/wallet", nil, 200) }

	// Own channel first, free even with a zero balance under billing.enforce
	// (no reservation, no ledger entry), although the platform channel has a
	// much higher priority.
	if code, _, raw := call(carolKey, "m1"); code != 200 || own.hits.Load() != 1 || plat.hits.Load()+plat2.hits.Load() != 0 {
		t.Fatalf("own = %d %s (own %d plat %d)", code, raw, own.hits.Load(), plat.hits.Load())
	}
	l := e.lastLog(e.admin, "model=m1")
	if l["channelTier"] != "own" || l["channelId"] != oID || l["charge"] != "0" || l["quotaCharge"] != "0" || l["cost"] != nil || l["subscriptionId"] != nil {
		t.Fatalf("own log = %v", l)
	}
	if w := wallet(e.carol); w["balance"] != "0" || w["reserved"] != "0" {
		t.Fatalf("wallet after own = %v", w)
	}
	if led := e.mustDo(e.carol, http.MethodGet, "/api/billing/ledger", nil, 200); len(led["items"].([]any)) != 0 {
		t.Fatalf("ledger after own = %v", led)
	}

	// Own fails (5xx) and the platform tier needs balance: 402.
	own.status.Store(500)
	if code, out, raw := call(carolKey, "m1"); code != 402 || errCode(out) != "insufficient_balance" || plat.hits.Load()+plat2.hits.Load() != 0 {
		t.Fatalf("own failed, zero balance = %d %s", code, raw)
	}
	if l := e.lastLog(e.admin, "model=m1"); l["channelTier"] != "own" || l["statusCode"].(float64) != 402 || l["charge"] != "0" {
		t.Fatalf("402 log = %v", l)
	}

	// With balance the request falls back to the platform tier and is billed.
	e.credit(carolID, "10")
	if code, _, raw := call(carolKey, "m1"); code != 200 || plat.hits.Load() != 1 {
		t.Fatalf("fallback to platform = %d %s (plat %d)", code, raw, plat.hits.Load())
	}
	l = e.lastLog(e.admin, "model=m1")
	// 10 input + 2 output tokens at 1000/M.
	if l["channelTier"] != "platform" || l["channelId"] != pID || l["charge"] != "0.012" || l["attempts"].(float64) != 2 || l["cost"] == nil {
		t.Fatalf("platform log = %v", l)
	}
	if w := wallet(e.carol); w["balance"] != "9.988" || w["reserved"] != "0" {
		t.Fatalf("wallet after platform = %v", w)
	}

	// Plan quotas: own requests do not count, platform requests do.
	own.status.Store(0)
	plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
		"name": "P", "description": "", "duration": "30d", "models": []string{"m1"}, "stackable": false,
		"rules": []map[string]any{{"id": "r", "label": "r", "meter": "requests", "window": map[string]any{"kind": "period", "every": "1d"}, "limit": "100"}},
	}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": carolID, "planId": plan["id"], "periods": 1}, 201)
	used := func() string {
		e.app.FlushLogs(context.Background())
		subs := e.mustDo(e.carol, http.MethodGet, "/api/billing/subscriptions", nil, 200)
		return subs["items"].([]any)[0].(map[string]any)["rules"].([]any)[0].(map[string]any)["used"].(string)
	}
	for i := 0; i < 2; i++ {
		if code, _, raw := call(carolKey, "m1"); code != 200 {
			t.Fatalf("own with plan = %d %s", code, raw)
		}
	}
	if u := used(); u != "0" {
		t.Fatalf("own requests counted against the plan: used %s", u)
	}
	own.status.Store(500)
	if code, _, raw := call(carolKey, "m1"); code != 200 {
		t.Fatalf("platform with plan = %d %s", code, raw)
	}
	if u := used(); u != "1" {
		t.Fatalf("platform request not counted: used %s", u)
	}
	if l := e.lastLog(e.admin, "model=m1"); l["channelTier"] != "platform" || l["subscriptionId"] == nil || l["charge"] != "0" || l["quotaCharge"] != "0.012" {
		t.Fatalf("plan log = %v", l)
	}
	own.status.Store(0)

	// Shared (user-to-user) channels are free and preferred over the platform.
	before := wallet(e.carol)["balance"]
	if code, _, raw := call(carolKey, "m2"); code != 200 || shared.hits.Load() != 1 {
		t.Fatalf("shared = %d %s (shared %d)", code, raw, shared.hits.Load())
	}
	if l := e.lastLog(e.admin, "model=m2"); l["channelTier"] != "shared" || l["channelId"] != sID || l["charge"] != "0" || l["cost"] != nil {
		t.Fatalf("shared log = %v", l)
	}
	if w := wallet(e.carol); w["balance"] != before {
		t.Fatalf("shared request billed: %v → %v", before, w["balance"])
	}
	// Bob's own channel is "own" for bob (and free with a zero balance).
	_, bobKey := e.key(bob, map[string]any{"name": "bk"})
	if code, _, raw := call(bobKey, "m2"); code != 200 || shared.hits.Load() != 2 {
		t.Fatalf("bob own = %d %s", code, raw)
	}
	if l := e.lastLog(bob, "model=m2"); l["channelTier"] != "own" || l["user"].(map[string]any)["id"] != bobID {
		t.Fatalf("bob log = %v", l)
	}

	// An administrator's own channels are "own" for the administrator.
	_, adminKey := e.key(e.admin, map[string]any{"name": "ak"})
	if code, _, raw := call(adminKey, "m1"); code != 200 {
		t.Fatalf("admin = %d %s", code, raw)
	}
	if l := e.lastLog(e.admin, "model=m1"); l["channelTier"] != "own" || l["charge"] != "0" {
		t.Fatalf("admin log = %v", l)
	}

	// Route rules only shape the platform tier: targets pin m1 to plat2, but
	// carol's own channel is still tried first; when it fails only plat2 is used.
	rule := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes", map[string]any{"name": "pin", "match": map[string]any{"models": []string{"m1"}, "roles": []string{"user"}},
		"targets": []map[string]any{{"channelId": p2ID}}, "retry": map[string]any{"maxAttempts": 1}}, 201)
	time.Sleep(20 * time.Millisecond)
	ownHits := own.hits.Load()
	if code, _, raw := call(carolKey, "m1"); code != 200 || own.hits.Load() != ownHits+1 || plat2.hits.Load() != 0 {
		t.Fatalf("rule + own = %d %s", code, raw)
	}
	own.status.Store(500)
	platHits := plat.hits.Load()
	if code, _, raw := call(carolKey, "m1"); code != 200 || plat2.hits.Load() != 1 || plat.hits.Load() != platHits {
		t.Fatalf("rule targets on platform tier = %d %s (plat2 %d, plat %d→%d)", code, raw, plat2.hits.Load(), platHits, plat.hits.Load())
	}
	if l := e.lastLog(e.admin, "model=m1"); l["channelTier"] != "platform" || l["channelId"] != p2ID || l["attempts"].(float64) != 2 {
		t.Fatalf("rule log = %v", l)
	}

	// Preview shows the tiers in attempt order (the rule only filters platform).
	pv := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "m1", "userId": carolID, "inbound": "openai.chat"}, 200)
	if strings.Join(toStrings(pv["tierOrder"]), ",") != "own,shared,platform" || pv["rule"].(map[string]any)["id"] != rule["id"] {
		t.Fatalf("preview = %v", pv)
	}
	var got []string
	for _, c := range pv["candidates"].([]any) {
		m := c.(map[string]any)
		got = append(got, m["channelName"].(string)+":"+m["tier"].(string))
	}
	if strings.Join(got, ",") != "carol-own:own,plat2:platform" {
		t.Fatalf("preview candidates = %v", got)
	}
	pv = e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "m2", "userId": carolID, "inbound": "openai.chat"}, 200)
	got = nil
	for _, c := range pv["candidates"].([]any) {
		m := c.(map[string]any)
		got = append(got, m["channelName"].(string)+":"+m["tier"].(string))
	}
	if strings.Join(got, ",") != "bob-shared:shared,plat:platform" {
		t.Fatalf("preview m2 candidates = %v", got)
	}

	// Fallback models follow the tiers too: m3 fails on the platform, the
	// fallback m2 is served by bob's shared channel (free; the hold taken for
	// m3 is released).
	e.mustDo(e.admin, http.MethodPost, "/api/admin/routes", map[string]any{"name": "fb", "match": map[string]any{"models": []string{"m3"}},
		"fallbackModels": []string{"m2"}}, 201)
	time.Sleep(20 * time.Millisecond)
	before = wallet(e.carol)["balance"]
	sharedHits := shared.hits.Load()
	if code, _, raw := call(carolKey, "m3"); code != 200 || shared.hits.Load() != sharedHits+1 {
		t.Fatalf("fallback to shared = %d %s", code, raw)
	}
	l = e.lastLog(e.admin, "model=m3")
	if l["channelTier"] != "shared" || l["servedModel"] != "m2" || l["charge"] != "0" {
		t.Fatalf("fallback log = %v", l)
	}
	if w := wallet(e.carol); w["balance"] != before || w["reserved"] != "0" {
		t.Fatalf("wallet after free fallback = %v (before %v)", w, before)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestModelInfoCRUD(t *testing.T) {
	e := setupGateway(t)
	path := "/api/admin/model-info/" + url.PathEscape("deepseek/chat")
	if !strings.Contains(path, "%2F") {
		t.Fatalf("path %s", path)
	}
	e.mustDo(e.carol, http.MethodGet, "/api/admin/model-info", nil, 403)
	e.mustDo(e.carol, http.MethodPut, path, map[string]any{}, 403)

	in := map[string]any{"displayName": " DeepSeek Chat ", "description": "通用对话", "vendor": "DeepSeek", "tags": []string{"chat", " Chat ", "", "代码"},
		"contextWindow": 128000, "maxOutput": 8192, "capabilities": map[string]any{"tools": true, "reasoning": false}, "hidden": false, "sortOrder": -1}
	created := e.mustDo(e.admin, http.MethodPut, path, in, 201)
	if created["model"] != "deepseek/chat" || created["displayName"] != "DeepSeek Chat" || created["version"].(float64) != 1 ||
		strings.Join(toStrings(created["tags"]), ",") != "chat,代码" || created["contextWindow"].(float64) != 128000 ||
		created["capabilities"].(map[string]any)["tools"] != true || created["capabilities"].(map[string]any)["vision"] != false ||
		created["sortOrder"].(float64) != -1 || created["updatedAt"] == nil {
		t.Fatalf("created = %v", created)
	}
	// Creating again (no version) or a stale version conflicts.
	if resp, out := e.admin.do(http.MethodPut, path, in); resp.StatusCode != 409 || errCode(out) != "version_conflict" {
		t.Fatalf("create twice = %d %v", resp.StatusCode, out)
	}
	in["version"] = 7
	e.mustDo(e.admin, http.MethodPut, path, in, 409)
	in["version"], in["hidden"], in["contextWindow"], in["maxOutput"] = 1, true, nil, nil
	upd := e.mustDo(e.admin, http.MethodPut, path, in, 200)
	if upd["version"].(float64) != 2 || upd["hidden"] != true || upd["contextWindow"] != nil {
		t.Fatalf("updated = %v", upd)
	}
	e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/plain", map[string]any{"version": 1}, 409) // never created
	e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/plain", map[string]any{}, 201)
	list := e.mustDo(e.admin, http.MethodGet, "/api/admin/model-info", nil, 200)
	items := list["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["model"] != "deepseek/chat" || items[1].(map[string]any)["displayName"] != "" ||
		len(items[1].(map[string]any)["tags"].([]any)) != 0 {
		t.Fatalf("list = %v", list)
	}

	// Validation: every key of the contract.
	resp, out := e.admin.do(http.MethodPut, "/api/admin/model-info/bad", map[string]any{
		"displayName": strings.Repeat("x", 101), "description": strings.Repeat("d", 1001), "vendor": strings.Repeat("v", 51),
		"tags": []string{strings.Repeat("t", 21)}, "contextWindow": -1, "maxOutput": 1.5, "sortOrder": "1",
		"capabilities": map[string]any{"vision": true, "audio": true}})
	details, _ := out["error"].(map[string]any)["details"].(map[string]any)
	for _, k := range []string{"displayName", "description", "vendor", "tags[0]", "contextWindow", "maxOutput", "sortOrder", "capabilities"} {
		if details[k] == nil {
			t.Errorf("missing detail %q", k)
		}
	}
	if resp.StatusCode != 422 {
		t.Fatalf("invalid = %d %v", resp.StatusCode, out)
	}
	tags := make([]string, 11)
	for i := range tags {
		tags[i] = "t" + string(rune('a'+i))
	}
	resp, out = e.admin.do(http.MethodPut, "/api/admin/model-info/bad", map[string]any{"tags": tags, "contextWindow": 100, "maxOutput": 200})
	details, _ = out["error"].(map[string]any)["details"].(map[string]any)
	if resp.StatusCode != 422 || details["tags"] == nil || details["maxOutput"] == nil {
		t.Fatalf("tags / maxOutput = %d %v", resp.StatusCode, out)
	}
	e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/bad", map[string]any{"capabilities": map[string]any{"vision": "yes"}}, 422)
	e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/"+url.PathEscape(" x"), map[string]any{}, 422)

	e.mustDo(e.admin, http.MethodDelete, path, nil, 204)
	e.mustDo(e.admin, http.MethodDelete, path, nil, 404)
	for action, want := range map[string]float64{"model_info.update": 3, "model_info.delete": 1} {
		a := e.mustDo(e.admin, http.MethodGet, "/api/admin/audit-logs?action="+action, nil, 200)
		if a["total"].(float64) != want {
			t.Errorf("%s audit entries = %v, want %v", action, a["total"], want)
		}
	}
}

func TestModelPlaza(t *testing.T) {
	e, bob := setupTiers(t)
	carolID := e.userID(e.carol)
	up := newModelUpstream(t, 0)
	// Global platform channels: openai (pub-chat, hidden-model) and anthropic (pub-claude).
	oaID := e.channel(e.admin, map[string]any{"name": "SECRET-GLOBAL-OA", "type": "openai", "baseUrl": pub(up.srv.URL) + "/v1", "scope": "global", "models": models("pub-chat", "hidden-model")})
	e.channel(e.admin, map[string]any{"name": "SECRET-GLOBAL-AN", "type": "anthropic", "baseUrl": pub(up.srv.URL), "scope": "global", "models": models("pub-claude", "pub-chat")})
	// Not in the platform plaza: alice's private channel, a platform channel
	// shared with carol, carol's own and bob's channel shared with carol.
	e.channel(e.admin, map[string]any{"name": "SECRET-PRIVATE", "type": "openai", "baseUrl": pub(up.srv.URL) + "/v1", "models": models("admin-private")})
	e.acceptShare(e.carol, e.channel(e.admin, map[string]any{"name": "SECRET-VIP", "type": "anthropic", "baseUrl": pub(up.srv.URL), "scope": "shared", "sharedWith": []string{carolID}, "models": models("vip-model")}))
	e.channel(e.carol, map[string]any{"name": "SECRET-CAROL", "type": "anthropic", "baseUrl": pub(up.srv.URL), "models": models("carol-model", "pub-chat")})
	e.acceptShare(e.carol, e.channel(bob, map[string]any{"name": "SECRET-BOB", "type": "openai", "baseUrl": pub(up.srv.URL) + "/v1", "scope": "shared", "sharedWith": []string{carolID}, "models": models("bob-model")}))

	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "pub-chat", "inputPerM": "1.5", "outputPerM": "2", "cacheReadPerM": "0.1"}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "pub-claude", "perRequest": "0.04"}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "cost", "model": "up-pub-chat", "channelId": oaID, "inputPerM": "0.123", "outputPerM": "0.456"}, 201)
	planAll := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{"name": "全部模型", "duration": "30d", "models": []string{},
		"rules": []map[string]any{{"id": "r", "label": "r", "meter": "requests", "window": map[string]any{"kind": "period", "every": "1d"}, "limit": "100"}}}, 201)
	planChat := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{"name": "Chat", "duration": "30d", "models": []string{"pub-chat"},
		"rules": []map[string]any{{"id": "r", "label": "r", "meter": "requests", "window": map[string]any{"kind": "period", "every": "1d"}, "limit": "100"}}}, 201)
	archived := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{"name": "Old", "duration": "30d", "models": []string{"pub-chat"}, "status": "archived",
		"rules": []map[string]any{{"id": "r", "label": "r", "meter": "requests", "window": map[string]any{"kind": "period", "every": "1d"}, "limit": "100"}}}, 201)
	e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/pub-chat", map[string]any{"displayName": "Pub Chat", "vendor": "OpenAI", "tags": []string{"chat"},
		"contextWindow": 64000, "capabilities": map[string]any{"tools": true}, "sortOrder": 5}, 201)
	e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/hidden-model", map[string]any{"hidden": true}, 201)
	time.Sleep(50 * time.Millisecond)

	anon := e.h.newClient()
	get := func(c *client, path string) (int, map[string]any, string) {
		req, _ := http.NewRequest(http.MethodGet, e.h.srv.URL+path, nil)
		resp, err := c.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return readBody(resp)
	}
	byModel := func(out map[string]any) map[string]map[string]any {
		m := map[string]map[string]any{}
		var order []string
		for _, it := range out["items"].([]any) {
			item := it.(map[string]any)
			m[item["model"].(string)] = item
			order = append(order, item["model"].(string))
		}
		m["_order"] = map[string]any{"v": strings.Join(order, ",")}
		return m
	}

	// Public by default.
	_, info := anon.do(http.MethodGet, "/api/system/info", nil)
	if info["publicModelPlaza"] != true {
		t.Fatalf("system info = %v", info)
	}
	code, out, raw := get(anon, "/api/plaza/models")
	if code != 200 {
		t.Fatalf("anonymous plaza = %d %s", code, raw)
	}
	if strings.Contains(raw, "SECRET") || strings.Contains(raw, "0.123") || strings.Contains(raw, "channel") {
		t.Fatalf("plaza leaks channel data: %s", raw)
	}
	items := byModel(out)
	if items["_order"]["v"] != "pub-claude,pub-chat" { // sortOrder 0 before 5
		t.Fatalf("plaza models = %v", items["_order"])
	}
	cur := out["currency"].(map[string]any)
	if cur["code"] != "USD" || cur["symbol"] != "$" || cur["decimals"].(float64) != 2 {
		t.Fatalf("currency = %v", cur)
	}
	pc := items["pub-chat"]
	price := pc["price"].(map[string]any)
	if pc["displayName"] != "Pub Chat" || pc["vendor"] != "OpenAI" || pc["contextWindow"].(float64) != 64000 || pc["maxOutput"] != nil ||
		pc["capabilities"].(map[string]any)["tools"] != true || price["inputPerM"] != "1.5" || price["outputPerM"] != "2" ||
		price["cacheReadPerM"] != "0.1" || price["cacheWritePerM"] != nil || price["perRequest"] != nil ||
		strings.Join(toStrings(pc["protocols"]), ",") != "openai.chat,openai.responses,anthropic.messages" {
		t.Fatalf("pub-chat = %v", pc)
	}
	var plans []string
	for _, p := range pc["plans"].([]any) {
		plans = append(plans, p.(map[string]any)["id"].(string))
	}
	if strings.Join(plans, ",") != planAll["id"].(string)+","+planChat["id"].(string) || strings.Contains(raw, archived["id"].(string)) {
		t.Fatalf("plans = %v", pc["plans"])
	}
	claude := items["pub-claude"]
	// Per-call pricing: only the fixed per-request fee is set.
	cp, _ := claude["price"].(map[string]any)
	if cp == nil || cp["perRequest"] != "0.04" || cp["inputPerM"] != "0" || claude["displayName"] != "" || len(claude["tags"].([]any)) != 0 || len(claude["plans"].([]any)) != 1 ||
		strings.Join(toStrings(claude["protocols"]), ",") != "openai.chat,openai.responses,anthropic.messages" {
		t.Fatalf("pub-claude = %v", claude)
	}

	// Private plaza: anonymous 401, signed-in users still see it.
	st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"site": map[string]any{"publicModelPlaza": false}}}, 200)
	if code, out, _ := get(anon, "/api/plaza/models"); code != 401 || errCode(out) != "unauthenticated" {
		t.Fatalf("private plaza, anonymous = %d %v", code, out)
	}
	if _, info := anon.do(http.MethodGet, "/api/system/info", nil); info["publicModelPlaza"] != false {
		t.Fatalf("system info = %v", info)
	}
	if code, out, _ := get(e.carol, "/api/plaza/models"); code != 200 || len(out["items"].([]any)) != 2 {
		t.Fatalf("private plaza, signed in = %d %v", code, out)
	}
	if code, _, _ := get(anon, "/api/plaza/mine"); code != 401 {
		t.Fatalf("anonymous mine = %d", code)
	}

	// My models: every callable model with tier counts.
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": carolID, "planId": planChat["id"], "periods": 1}, 201)
	code, out, raw = get(e.carol, "/api/plaza/mine")
	if code != 200 || strings.Contains(raw, "SECRET") {
		t.Fatalf("mine = %d %s", code, raw)
	}
	mine := byModel(out)
	if mine["_order"]["v"] != "bob-model,carol-model,hidden-model,pub-claude,vip-model,pub-chat" {
		t.Fatalf("mine models = %v", mine["_order"])
	}
	src := func(m string) string {
		s := mine[m]["sources"].(map[string]any)
		return strings.Join([]string{ftoa(s["own"]), ftoa(s["shared"]), ftoa(s["platform"])}, "/") + " " + mine[m]["billing"].(string)
	}
	for m, want := range map[string]string{"pub-chat": "1/0/2 free", "carol-model": "1/0/0 free", "bob-model": "0/1/0 free",
		"vip-model": "0/0/1 platform", "hidden-model": "0/0/1 platform", "pub-claude": "0/0/1 platform"} {
		if got := src(m); got != want {
			t.Errorf("%s sources = %s, want %s", m, got, want)
		}
	}
	if sub := mine["pub-chat"]["subscription"].(map[string]any); sub["planName"] != "Chat" || sub["id"] == nil {
		t.Fatalf("pub-chat subscription = %v", sub)
	}
	if mine["vip-model"]["subscription"] != nil || mine["pub-chat"]["price"] == nil ||
		// No model info → a chat model: embeddings are listed only when marked.
		strings.Join(toStrings(mine["bob-model"]["protocols"]), ",") != "openai.chat,openai.responses,anthropic.messages" ||
		strings.Contains(strings.Join(toStrings(mine["carol-model"]["protocols"]), ","), "openai.embeddings") {
		t.Fatalf("mine = %v", out)
	}
	if out["currency"].(map[string]any)["code"] != "USD" {
		t.Fatalf("mine currency = %v", out["currency"])
	}
}

func ftoa(v any) string {
	f, _ := v.(float64)
	return strconv.Itoa(int(f))
}

// Plaza protocols follow the model info capabilities: a pure image or
// embedding model lists only its own endpoint, chat models never list
// embeddings unless marked.
func TestPlazaProtocolsFollowCapabilities(t *testing.T) {
	e := setupGateway(t)
	up := newModelUpstream(t, 0)
	e.channel(e.admin, map[string]any{"name": "oa", "type": "openai", "baseUrl": up.srv.URL + "/v1", "scope": "global",
		"models": models("img-model", "emb-model", "chat-model", "plain-model")})
	none := map[string]bool{"vision": false, "tools": false, "reasoning": false, "embedding": false, "imageGeneration": false, "audioInput": false, "audioOutput": false}
	with := func(k string) map[string]bool {
		c := map[string]bool{}
		for n, v := range none {
			c[n] = v
		}
		c[k] = true
		return c
	}
	for model, caps := range map[string]map[string]bool{"img-model": with("imageGeneration"), "emb-model": with("embedding"), "chat-model": with("tools")} {
		e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/"+model, map[string]any{"displayName": "", "description": "", "vendor": "",
			"tags": []string{}, "contextWindow": nil, "maxOutput": nil, "capabilities": caps, "hidden": false, "sortOrder": 0}, 201)
	}
	out := e.mustDo(e.admin, http.MethodGet, "/api/plaza/models", nil, 200)
	got := map[string]string{}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		got[m["model"].(string)] = strings.Join(toStrings(m["protocols"]), ",")
	}
	for model, want := range map[string]string{
		"img-model":   "openai.images",
		"emb-model":   "openai.embeddings",
		"chat-model":  "openai.chat,openai.responses,anthropic.messages",
		"plain-model": "openai.chat,openai.responses,anthropic.messages",
	} {
		if got[model] != want {
			t.Errorf("%s protocols = %q, want %q", model, got[model], want)
		}
	}
}
