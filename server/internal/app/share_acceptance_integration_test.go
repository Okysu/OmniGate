package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Security revision (docs/contracts/phase5-api.md §5): user-to-user channel
// shares require the recipient's acceptance, group shares are admin-only.

// acceptShare accepts the share of channelID as c and waits for the
// registry reload.
func (e *gwEnv) acceptShare(c *client, channelID string) map[string]any {
	e.t.Helper()
	out := e.mustDo(c, http.MethodPost, "/api/channel-shares/"+channelID+"/accept", nil, 200)
	time.Sleep(50 * time.Millisecond) // registry reloads asynchronously
	return out
}

// shareGroups lets the administrator (channels.manage) share channelID with
// groups, keeping its user shares.
func (e *gwEnv) shareGroups(channelID string, groups ...string) {
	e.t.Helper()
	ch := e.mustDo(e.admin, http.MethodGet, "/api/channels/"+channelID, nil, 200)
	users := ch["sharedWith"].(map[string]any)["users"]
	e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+channelID, map[string]any{"scope": "shared",
		"sharedWith": map[string]any{"users": users, "groups": groups}, "version": ch["version"]}, 200)
	time.Sleep(50 * time.Millisecond)
}

// v1Models returns the model ids /v1/models lists for key.
func v1Models(t *testing.T, base, key string) []string {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	code, out, raw := readBody(resp)
	if code != 200 {
		t.Fatalf("/v1/models = %d %s", code, raw)
	}
	var ids []string
	for _, m := range out["data"].([]any) {
		ids = append(ids, m.(map[string]any)["id"].(string))
	}
	return ids
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// mineByModel indexes GET /api/plaza/mine items by model.
func mineByModel(out map[string]any) map[string]map[string]any {
	m := map[string]map[string]any{}
	for _, it := range out["items"].([]any) {
		x := it.(map[string]any)
		m[x["model"].(string)] = x
	}
	return m
}

// TestShareAcceptanceAuditPoC reproduces the audit: a regular user (bob)
// points a channel at his own server and shares it with carol and with the
// default group. Carol's requests must not reach it until she accepts, and
// group sharing by a regular user is refused.
func TestShareAcceptanceAuditPoC(t *testing.T) {
	e, bob := setupTiers(t)
	base := e.h.srv.URL
	carolID, bobID := e.userID(e.carol), e.userID(bob)
	plat, evil := newModelUpstream(t, 0), newModelUpstream(t, 0)
	platID := e.channel(e.admin, map[string]any{"name": "plat", "type": "openai", "baseUrl": pub(plat.srv.URL) + "/v1", "scope": "global",
		"models": models("gpt")})
	_, carolKey := e.key(e.carol, map[string]any{"name": "ck"})
	call := func(key, model string) (int, string) {
		code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chat(model)))
		return code, raw
	}
	const defaultGroup = "01920000-0000-7000-8000-000000000001" // fixed id (migration 00012)
	evilBody := func() map[string]any {
		return map[string]any{"name": "evil", "type": "openai", "baseUrl": pub(evil.srv.URL) + "/v1", "scope": "shared", "priority": 1000,
			"apiKey": "sk-evil-0123456789", "models": models("gpt", "evil-only")}
	}

	// Group sharing by a regular user: 422, on create and on update.
	b := evilBody()
	b["sharedWith"] = map[string]any{"users": []string{carolID}, "groups": []string{defaultGroup}}
	resp, out := bob.do(http.MethodPost, "/api/channels", b)
	if resp.StatusCode != 422 || out["error"].(map[string]any)["details"].(map[string]any)["sharedWith.groups"] == nil {
		t.Fatalf("group share by a regular user = %d %v", resp.StatusCode, out)
	}
	b = evilBody()
	b["sharedWith"] = []string{carolID}
	evilID := e.channel(bob, b)
	ch := e.mustDo(bob, http.MethodGet, "/api/channels/"+evilID, nil, 200)
	resp, out = bob.do(http.MethodPatch, "/api/channels/"+evilID, map[string]any{"version": ch["version"],
		"sharedWith": map[string]any{"users": []string{carolID}, "groups": []string{defaultGroup}}})
	if resp.StatusCode != 422 || out["error"].(map[string]any)["details"].(map[string]any)["sharedWith.groups"] == nil {
		t.Fatalf("group share update by a regular user = %d %v", resp.StatusCode, out)
	}

	// The pending invitation grants nothing: carol's requests go to the
	// platform, the channel is invisible to her except as an invitation.
	if code, raw := call(carolKey, "gpt"); code != 200 || evil.hits.Load() != 0 || plat.hits.Load() != 1 {
		t.Fatalf("pending share = %d %s (evil %d)", code, raw, evil.hits.Load())
	}
	if l := e.lastLog(e.admin, "model=gpt"); l["channelId"] != platID || l["channelTier"] != "platform" {
		t.Fatalf("pending share log = %v", l)
	}
	if code, _ := call(carolKey, "evil-only"); code != 404 {
		t.Fatalf("evil-only before acceptance = %d", code)
	}
	if ids := v1Models(t, base, carolKey); slices.Contains(ids, "evil-only") {
		t.Fatalf("/v1/models lists a pending share: %v", ids)
	}
	if raw := e.mustDo(e.carol, http.MethodGet, "/api/plaza/mine", nil, 200); strings.Contains(toJSON(raw), "evil-only") {
		t.Fatalf("plaza mine lists a pending share: %v", raw)
	}
	pv := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "gpt", "userId": carolID, "inbound": "openai.chat"}, 200)
	if strings.Contains(toJSON(pv), evilID) {
		t.Fatalf("preview lists a pending share: %v", pv)
	}
	e.mustDo(e.carol, http.MethodGet, "/api/channels/"+evilID, nil, 404)
	if strings.Contains(toJSON(e.mustDo(e.carol, http.MethodGet, "/api/channels", nil, 200)), evilID) {
		t.Fatal("channel list shows a pending share")
	}
	// Other users (alice, in the default group) are unaffected.
	_, adminKey := e.key(e.admin, map[string]any{"name": "ak"})
	if code, raw := call(adminKey, "gpt"); code != 200 || evil.hits.Load() != 0 {
		t.Fatalf("admin request = %d %s (evil %d)", code, raw, evil.hits.Load())
	}

	// Carol sees the invitation (no secrets) and was notified.
	inc := e.mustDo(e.carol, http.MethodGet, "/api/channel-shares", nil, 200)["items"].([]any)
	if len(inc) != 1 {
		t.Fatalf("incoming = %v", inc)
	}
	it := inc[0].(map[string]any)
	if it["channelId"] != evilID || it["status"] != "pending" || it["name"] != "evil" || it["type"] != "openai" || it["channelStatus"] != "enabled" ||
		it["owner"].(map[string]any)["id"] != bobID || strings.Join(toStrings(it["models"]), ",") != "gpt,evil-only" || it["respondedAt"] != nil {
		t.Fatalf("incoming share = %v", it)
	}
	if raw := toJSON(inc); strings.Contains(raw, "baseUrl") || strings.Contains(raw, "upstream.example.com") || strings.Contains(raw, "sk-evil") ||
		strings.Contains(raw, "up-gpt") {
		t.Fatalf("incoming share leaks channel config: %s", raw)
	}
	n := e.expectNotifs(e.carol, "channel.share_invited", 1)[0]
	if n["link"] != "/console/channels?tab=shared" || n["data"].(map[string]any)["channelId"] != evilID ||
		!strings.Contains(n["body"].(string), "请求与响应") {
		t.Fatalf("invitation notification = %v", n)
	}
	e.expectNotifs(e.admin, "channel.share_invited", 0)
	// The owner sees the pending status per recipient.
	ch = e.mustDo(bob, http.MethodGet, "/api/channels/"+evilID, nil, 200)
	shares := ch["shares"].([]any)
	if len(shares) != 1 || shares[0].(map[string]any)["userId"] != carolID || shares[0].(map[string]any)["status"] != "pending" ||
		shares[0].(map[string]any)["displayName"] == "" {
		t.Fatalf("owner shares = %v", shares)
	}

	// Wrong transitions.
	e.mustDo(e.carol, http.MethodPost, "/api/channel-shares/"+evilID+"/leave", nil, 409)
	e.mustDo(e.admin, http.MethodPost, "/api/channel-shares/"+evilID+"/accept", nil, 404)
	e.mustDo(e.carol, http.MethodPost, "/api/channel-shares/"+platID+"/accept", nil, 404)
	e.mustDo(e.carol, http.MethodPost, "/api/channel-shares/not-a-uuid/accept", nil, 404)

	// Decline: the owner sees it, nothing changes for carol.
	e.mustDo(e.carol, http.MethodPost, "/api/channel-shares/"+evilID+"/decline", nil, 204)
	e.mustDo(e.carol, http.MethodPost, "/api/channel-shares/"+evilID+"/accept", nil, 404)
	if inc := e.mustDo(e.carol, http.MethodGet, "/api/channel-shares", nil, 200)["items"].([]any); len(inc) != 0 {
		t.Fatalf("incoming after decline = %v", inc)
	}
	ch = e.mustDo(bob, http.MethodGet, "/api/channels/"+evilID, nil, 200)
	if sh := ch["shares"].([]any)[0].(map[string]any); sh["status"] != "declined" || sh["respondedAt"] == nil ||
		len(ch["sharedWith"].(map[string]any)["users"].([]any)) != 0 {
		t.Fatalf("owner view after decline = %v / %v", ch["shares"], ch["sharedWith"])
	}
	// A routine update keeps the refusal.
	ch = e.mustDo(bob, http.MethodPatch, "/api/channels/"+evilID, map[string]any{"name": "evil2", "version": ch["version"],
		"sharedWith": ch["sharedWith"]}, 200)
	if sh := ch["shares"].([]any)[0].(map[string]any); sh["status"] != "declined" {
		t.Fatalf("declined share after a routine update = %v", sh)
	}
	if code, _ := call(carolKey, "gpt"); code != 200 || evil.hits.Load() != 0 {
		t.Fatalf("after decline: evil hits %d", evil.hits.Load())
	}

	// Re-inviting creates a new pending invitation (the notification is
	// throttled within an hour).
	ch = e.mustDo(bob, http.MethodPatch, "/api/channels/"+evilID, map[string]any{"version": ch["version"],
		"sharedWith": map[string]any{"users": []string{carolID}}}, 200)
	if sh := ch["shares"].([]any)[0].(map[string]any); sh["status"] != "pending" || sh["respondedAt"] != nil {
		t.Fatalf("re-invited share = %v", sh)
	}
	if inc := e.mustDo(e.carol, http.MethodGet, "/api/channel-shares", nil, 200)["items"].([]any); len(inc) != 1 ||
		inc[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("incoming after re-invite = %v", inc)
	}
	e.expectNotifs(e.carol, "channel.share_invited", 1)
	if code, _ := call(carolKey, "gpt"); code != 200 || evil.hits.Load() != 0 {
		t.Fatalf("after re-invite: evil hits %d", evil.hits.Load())
	}

	// Accepting is explicit consent: the channel is now tried before the
	// platform, free of charge.
	acc := e.acceptShare(e.carol, evilID)
	if acc["status"] != "accepted" || acc["respondedAt"] == nil {
		t.Fatalf("accept = %v", acc)
	}
	e.mustDo(e.carol, http.MethodPost, "/api/channel-shares/"+evilID+"/accept", nil, 200) // idempotent
	e.mustDo(e.carol, http.MethodPost, "/api/channel-shares/"+evilID+"/decline", nil, 409)
	if code, raw := call(carolKey, "gpt"); code != 200 || evil.hits.Load() != 1 {
		t.Fatalf("accepted share = %d %s (evil %d)", code, raw, evil.hits.Load())
	}
	if l := e.lastLog(e.admin, "model=gpt"); l["channelId"] != evilID || l["channelTier"] != "shared" || l["charge"] != "0" {
		t.Fatalf("accepted share log = %v", l)
	}
	if ids := v1Models(t, base, carolKey); !slices.Contains(ids, "evil-only") {
		t.Fatalf("/v1/models misses the accepted share: %v", ids)
	}
	mine := mineByModel(e.mustDo(e.carol, http.MethodGet, "/api/plaza/mine", nil, 200))
	if s := mine["evil-only"]; s == nil || s["billing"] != "free" || s["sources"].(map[string]any)["shared"] != float64(1) {
		t.Fatalf("plaza mine evil-only = %v", s)
	}
	pv = e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "gpt", "userId": carolID, "inbound": "openai.chat"}, 200)
	if c := pv["candidates"].([]any); len(c) < 2 || c[0].(map[string]any)["channelId"] != evilID || c[0].(map[string]any)["tier"] != "shared" {
		t.Fatalf("preview after accept = %v", pv)
	}
	if v := e.mustDo(e.carol, http.MethodGet, "/api/channels/"+evilID, nil, 200); v["baseUrl"] != nil || v["shares"] != nil {
		t.Fatalf("recipient view = %v", v)
	}
	if n, _ := e.auditCount("channel.share_accept"); n != 1 {
		t.Fatalf("share_accept audit entries = %d", n)
	}

	// A key policy restricted to the platform channel never uses the share.
	_, restricted := e.key(e.carol, map[string]any{"name": "r", "policy": map[string]any{"allowedChannels": []string{platID}}})
	if code, raw := call(restricted, "gpt"); code != 200 || evil.hits.Load() != 1 {
		t.Fatalf("restricted key = %d %s (evil %d)", code, raw, evil.hits.Load())
	}
	if code, _ := call(restricted, "evil-only"); code != 404 {
		t.Fatalf("restricted key, evil-only = %d", code)
	}

	// Disabling the owner stops the share; re-enabling restores it.
	setStatus := func(status string) {
		u := e.user(bobID)
		body := map[string]any{"status": status, "version": u["user"].(map[string]any)["version"]}
		if status == "disabled" {
			body["disabledReason"] = "abuse"
		}
		e.mustDo(e.admin, http.MethodPatch, "/api/admin/users/"+bobID, body, 200)
		time.Sleep(50 * time.Millisecond)
	}
	setStatus("disabled")
	if code, raw := call(carolKey, "gpt"); code != 200 || evil.hits.Load() != 1 {
		t.Fatalf("disabled owner = %d %s (evil %d)", code, raw, evil.hits.Load())
	}
	if code, _ := call(carolKey, "evil-only"); code != 404 {
		t.Fatalf("disabled owner, evil-only = %d", code)
	}
	setStatus("active")
	bob.login("bob") // disabling revoked bob's sessions
	if code, _ := call(carolKey, "gpt"); code != 200 || evil.hits.Load() != 2 {
		t.Fatalf("re-enabled owner: evil hits %d", evil.hits.Load())
	}

	// Leaving ends the share at once.
	e.mustDo(e.carol, http.MethodPost, "/api/channel-shares/"+evilID+"/leave", nil, 204)
	time.Sleep(50 * time.Millisecond)
	if code, _ := call(carolKey, "gpt"); code != 200 || evil.hits.Load() != 2 {
		t.Fatalf("after leave: evil hits %d", evil.hits.Load())
	}
	if sh := e.mustDo(bob, http.MethodGet, "/api/channels/"+evilID, nil, 200)["shares"].([]any)[0].(map[string]any); sh["status"] != "declined" {
		t.Fatalf("owner view after leave = %v", sh)
	}
	if n, _ := e.auditCount("channel.share_leave"); n != 1 {
		t.Fatalf("share_leave audit entries = %d", n)
	}

	// Making the channel private drops every share record.
	ch = e.mustDo(bob, http.MethodGet, "/api/channels/"+evilID, nil, 200)
	ch = e.mustDo(bob, http.MethodPatch, "/api/channels/"+evilID, map[string]any{"scope": "private", "version": ch["version"]}, 200)
	if len(ch["shares"].([]any)) != 0 {
		t.Fatalf("shares after private = %v", ch["shares"])
	}
}

// TestGroupSharesAdminOnly: group shares set by a channels.manage
// administrator need no acceptance; regular owners may keep or remove them.
func TestGroupSharesAdminOnly(t *testing.T) {
	e, bob := setupTiers(t)
	base := e.h.srv.URL
	const defaultGroup = "01920000-0000-7000-8000-000000000001"
	adminUp, bobUp := newModelUpstream(t, 0), newModelUpstream(t, 0)
	_, carolKey := e.key(e.carol, map[string]any{"name": "ck"})
	call := func(model string) int {
		code, _, _ := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", carolKey, chat(model)))
		return code
	}
	// An administrator's channel shared with the default group: usable at once.
	e.channel(e.admin, map[string]any{"name": "admin-g", "type": "openai", "baseUrl": pub(adminUp.srv.URL) + "/v1", "scope": "shared",
		"sharedWith": map[string]any{"groups": []string{defaultGroup}}, "models": models("g1")})
	if code := call("g1"); code != 200 || adminUp.hits.Load() != 1 {
		t.Fatalf("admin group share = %d", code)
	}
	if l := e.lastLog(e.admin, "model=g1"); l["channelTier"] != "platform" {
		t.Fatalf("admin group share log = %v", l)
	}
	// An administrator shares bob's channel with the group: shared tier for
	// carol without acceptance (an administrator's decision).
	bID := e.channel(bob, map[string]any{"name": "bob-g", "type": "openai", "baseUrl": pub(bobUp.srv.URL) + "/v1", "scope": "shared",
		"models": models("g2")})
	if code := call("g2"); code != 404 {
		t.Fatalf("before group share = %d", code)
	}
	e.shareGroups(bID, defaultGroup)
	if code := call("g2"); code != 200 || bobUp.hits.Load() != 1 {
		t.Fatalf("admin-set group share = %d", code)
	}
	if l := e.lastLog(e.admin, "model=g2"); l["channelTier"] != "shared" || l["charge"] != "0" {
		t.Fatalf("admin-set group share log = %v", l)
	}
	if inc := e.mustDo(e.carol, http.MethodGet, "/api/channel-shares", nil, 200)["items"].([]any); len(inc) != 0 {
		t.Fatalf("group shares are not invitations: %v", inc)
	}
	// Bob may resubmit (keep) or remove the group, not add one.
	ch := e.mustDo(bob, http.MethodGet, "/api/channels/"+bID, nil, 200)
	ch = e.mustDo(bob, http.MethodPatch, "/api/channels/"+bID, map[string]any{"name": "bob-g2", "sharedWith": ch["sharedWith"], "version": ch["version"]}, 200)
	other := e.newGroup(map[string]any{"name": "other"})
	resp, out := bob.do(http.MethodPatch, "/api/channels/"+bID, map[string]any{"version": ch["version"],
		"sharedWith": map[string]any{"groups": []string{defaultGroup, other}}})
	if resp.StatusCode != 422 || out["error"].(map[string]any)["details"].(map[string]any)["sharedWith.groups"] == nil {
		t.Fatalf("adding a group = %d %v", resp.StatusCode, out)
	}
	e.mustDo(bob, http.MethodPatch, "/api/channels/"+bID, map[string]any{"version": ch["version"], "sharedWith": map[string]any{"groups": []string{}}}, 200)
	time.Sleep(50 * time.Millisecond)
	if code := call("g2"); code != 404 {
		t.Fatalf("after removing the group = %d", code)
	}
}
