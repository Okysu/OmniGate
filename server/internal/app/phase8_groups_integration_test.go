package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Round 6 (docs/contracts/phase8-api.md §1): user groups, price multipliers
// and group sharing.

func (e *gwEnv) groupList() []map[string]any {
	e.t.Helper()
	out := e.mustDo(e.admin, http.MethodGet, "/api/admin/groups", nil, 200)
	var list []map[string]any
	for _, it := range out["items"].([]any) {
		list = append(list, it.(map[string]any))
	}
	return list
}

func (e *gwEnv) group(name string) map[string]any {
	e.t.Helper()
	for _, g := range e.groupList() {
		if g["name"] == name {
			return g
		}
	}
	e.t.Fatalf("group %q not found", name)
	return nil
}

func (e *gwEnv) newGroup(body map[string]any) string {
	e.t.Helper()
	return e.mustDo(e.admin, http.MethodPost, "/api/admin/groups", body, 201)["id"].(string)
}

func (e *gwEnv) patchGroup(id string, body map[string]any) map[string]any {
	e.t.Helper()
	for _, g := range e.groupList() {
		if g["id"] == id {
			body["version"] = g["version"]
		}
	}
	return e.mustDo(e.admin, http.MethodPatch, "/api/admin/groups/"+id, body, 200)
}

func (e *gwEnv) moveUser(userID, groupID string) {
	e.t.Helper()
	u := e.mustDo(e.admin, http.MethodPut, "/api/admin/users/"+userID+"/group", map[string]any{"groupId": groupID}, 200)
	if u["group"].(map[string]any)["id"] != groupID {
		e.t.Fatalf("moved user = %v", u)
	}
	time.Sleep(50 * time.Millisecond) // registry reload (group shares)
}

func userGroupOf(u map[string]any) string {
	g, _ := u["group"].(map[string]any)
	if g == nil {
		return ""
	}
	return g["name"].(string)
}

func TestUserGroupsAdmin(t *testing.T) {
	e := setupGateway(t)
	aliceID, carolID := e.userID(e.admin), e.userID(e.carol)

	// The migration's default group holds every user.
	list := e.groupList()
	if len(list) != 1 {
		t.Fatalf("groups = %v", list)
	}
	def := list[0]
	if def["name"] != "默认" || def["isDefault"] != true || def["priceMultiplier"] != "1" || def["members"] != float64(2) ||
		def["timezone"] != "Asia/Shanghai" || def["version"] != float64(1) || def["description"] != "" {
		t.Fatalf("default group = %v", def)
	}
	if l := def["limits"].(map[string]any); l["rpm"] != nil || l["rpd"] != nil || l["dailySpend"] != nil || l["monthlySpend"] != nil {
		t.Fatalf("default limits = %v", l)
	}
	defID := def["id"].(string)
	_, me := e.carol.do(http.MethodGet, "/api/me", nil)
	if g := me["group"].(map[string]any); g["id"] != defID || g["name"] != "默认" || g["priceMultiplier"] != "1" || g["limits"] == nil {
		t.Fatalf("/api/me group = %v", me["group"])
	}
	if userGroupOf(me["user"].(map[string]any)) != "默认" {
		t.Fatalf("/api/me user = %v", me["user"])
	}
	users := e.mustDo(e.admin, http.MethodGet, "/api/admin/users", nil, 200)
	for _, it := range users["items"].([]any) {
		if userGroupOf(it.(map[string]any)) != "默认" {
			t.Fatalf("user without group: %v", it)
		}
	}
	if d := e.user(carolID); userGroupOf(d["user"].(map[string]any)) != "默认" {
		t.Fatalf("detail = %v", d["user"])
	}

	t.Run("permissions and validation", func(t *testing.T) {
		e.mustDo(e.carol, http.MethodGet, "/api/admin/groups", nil, 403)
		e.mustDo(e.carol, http.MethodPost, "/api/admin/groups", map[string]any{"name": "x"}, 403)
		e.mustDo(e.carol, http.MethodPut, "/api/admin/users/"+carolID+"/group", map[string]any{"groupId": defID}, 403)
		for _, c := range []struct {
			body  map[string]any
			field string
		}{
			{map[string]any{}, "name"},
			{map[string]any{"name": strings.Repeat("x", 51)}, "name"},
			{map[string]any{"name": "a", "description": strings.Repeat("x", 201)}, "description"},
			{map[string]any{"name": "a", "priceMultiplier": "100.5"}, "priceMultiplier"},
			{map[string]any{"name": "a", "priceMultiplier": "-1"}, "priceMultiplier"},
			{map[string]any{"name": "a", "priceMultiplier": "0.1234567891"}, "priceMultiplier"},
			{map[string]any{"name": "a", "timezone": "Mars/Base"}, "timezone"},
			{map[string]any{"name": "a", "limits": map[string]any{"rpm": 0}}, "limits.rpm"},
			{map[string]any{"name": "a", "limits": map[string]any{"rpd": -1}}, "limits.rpd"},
			{map[string]any{"name": "a", "limits": map[string]any{"dailySpend": "abc"}}, "limits.dailySpend"},
			{map[string]any{"name": "a", "limits": map[string]any{"monthlySpend": "-2"}}, "limits.monthlySpend"},
		} {
			out := e.mustDo(e.admin, http.MethodPost, "/api/admin/groups", c.body, 422)
			if out["error"].(map[string]any)["details"].(map[string]any)[c.field] == nil {
				t.Fatalf("%v: details = %v", c.body, out)
			}
		}
		if out := e.mustDo(e.admin, http.MethodPost, "/api/admin/groups", map[string]any{"name": " 默认 "}, 409); errCode(out) != "group_name_exists" {
			t.Fatalf("duplicate name = %v", out)
		}
		e.mustDo(e.admin, http.MethodPatch, "/api/admin/groups/"+defID, map[string]any{"name": "x"}, 422) // version missing
		e.mustDo(e.admin, http.MethodPatch, "/api/admin/groups/"+defID, map[string]any{"name": "x", "version": 9}, 409)
		e.mustDo(e.admin, http.MethodPatch, "/api/admin/groups/01920000-0000-7000-8000-00000000ffff", map[string]any{"version": 1}, 404)
		// The default group cannot be un-defaulted directly.
		e.mustDo(e.admin, http.MethodPatch, "/api/admin/groups/"+defID, map[string]any{"isDefault": false, "version": 1}, 422)
	})

	var vipID string
	t.Run("create, move users, switch default", func(t *testing.T) {
		g := e.mustDo(e.admin, http.MethodPost, "/api/admin/groups", map[string]any{"name": "VIP", "description": "八折",
			"priceMultiplier": 0.8, "timezone": "UTC",
			"limits": map[string]any{"rpm": 60, "rpd": 1000, "dailySpend": "5", "monthlySpend": 100}}, 201)
		vipID = g["id"].(string)
		if g["priceMultiplier"] != "0.8" || g["isDefault"] != false || g["members"] != float64(0) || g["timezone"] != "UTC" {
			t.Fatalf("created = %v", g)
		}
		if l := g["limits"].(map[string]any); l["rpm"] != float64(60) || l["rpd"] != float64(1000) || l["dailySpend"] != "5" || l["monthlySpend"] != "100" {
			t.Fatalf("limits = %v", l)
		}
		e.mustDo(e.admin, http.MethodPut, "/api/admin/users/"+carolID+"/group", map[string]any{"groupId": "nope"}, 422)
		e.mustDo(e.admin, http.MethodPut, "/api/admin/users/"+carolID+"/group", map[string]any{}, 422)
		e.mustDo(e.admin, http.MethodPut, "/api/admin/users/"+carolID+"/group", map[string]any{"groupId": "01920000-0000-7000-8000-00000000ffff"}, 422)
		e.mustDo(e.admin, http.MethodPut, "/api/admin/users/01920000-0000-7000-8000-00000000ffff/group", map[string]any{"groupId": vipID}, 404)
		e.moveUser(carolID, vipID)
		if g := e.group("VIP"); g["members"] != float64(1) {
			t.Fatalf("members = %v", g)
		}
		filtered := e.mustDo(e.admin, http.MethodGet, "/api/admin/users?groupId="+vipID, nil, 200)
		if filtered["total"] != float64(1) || filtered["items"].([]any)[0].(map[string]any)["id"] != carolID {
			t.Fatalf("filter by group = %v", filtered)
		}
		e.mustDo(e.admin, http.MethodGet, "/api/admin/users?groupId=x", nil, 422)
		_, me := e.carol.do(http.MethodGet, "/api/me", nil)
		if g := me["group"].(map[string]any); g["name"] != "VIP" || g["priceMultiplier"] != "0.8" ||
			g["limits"].(map[string]any)["dailySpend"] != "5" {
			t.Fatalf("/api/me group = %v", g)
		}
		// Moving into the current group changes nothing (no audit, no notification).
		e.moveUser(carolID, vipID)
		n := e.expectNotifs(e.carol, "account.group_changed", 1)
		if !strings.Contains(n[0]["body"].(string), "VIP") || !strings.Contains(n[0]["body"].(string), "×0.8") {
			t.Fatalf("notification = %v", n[0])
		}
		if c, items := e.auditCount("user.group_change"); c != 1 || !strings.Contains(stringify(items[0]), vipID) {
			t.Fatalf("audit user.group_change = %d %v", c, items)
		}

		// Switching the default: VIP becomes the default, new users join it.
		out := e.patchGroup(vipID, map[string]any{"isDefault": true})
		if out["isDefault"] != true || out["members"] != float64(1) {
			t.Fatalf("patched = %v", out)
		}
		if g := e.group("默认"); g["isDefault"] != false || g["version"] != float64(2) {
			t.Fatalf("old default = %v", g)
		}
		dave := e.h.newClient()
		dave.login("bob")
		if _, me := dave.do(http.MethodGet, "/api/me", nil); userGroupOf(me["user"].(map[string]any)) != "VIP" {
			t.Fatalf("new user group = %v", me["user"])
		}
		if out := e.mustDo(e.admin, http.MethodDelete, "/api/admin/groups/"+vipID, nil, 409); errCode(out) != "group_is_default" {
			t.Fatalf("delete default = %v", out)
		}
		// A partial PATCH keeps the other fields; limits replace all four.
		out = e.patchGroup(vipID, map[string]any{"limits": map[string]any{"rpm": 10}})
		if out["priceMultiplier"] != "0.8" || out["description"] != "八折" {
			t.Fatalf("partial patch = %v", out)
		}
		if l := out["limits"].(map[string]any); l["rpm"] != float64(10) || l["rpd"] != nil || l["dailySpend"] != nil {
			t.Fatalf("limits after patch = %v", l)
		}
		e.patchGroup(defID, map[string]any{"isDefault": true})
		if g := e.group("VIP"); g["isDefault"] != false || g["members"] != float64(2) {
			t.Fatalf("VIP after switching back = %v", g)
		}
	})

	t.Run("batch set_group", func(t *testing.T) {
		g2 := e.newGroup(map[string]any{"name": "G2"})
		bobID := ""
		for _, it := range e.mustDo(e.admin, http.MethodGet, "/api/admin/users?q=bob", nil, 200)["items"].([]any) {
			bobID = it.(map[string]any)["id"].(string)
		}
		e.mustDo(e.admin, http.MethodPost, "/api/admin/users/batch", map[string]any{"ids": []string{carolID}, "action": "set_group"}, 422)
		e.mustDo(e.admin, http.MethodPost, "/api/admin/users/batch", map[string]any{"ids": []string{carolID}, "action": "set_group",
			"groupId": "01920000-0000-7000-8000-00000000ffff"}, 422)
		res := e.mustDo(e.admin, http.MethodPost, "/api/admin/users/batch", map[string]any{"ids": []string{carolID, bobID, aliceID, "bogus"},
			"action": "set_group", "groupId": g2}, 200)
		if len(res["succeeded"].([]any)) != 3 || len(res["failed"].([]any)) != 1 || res["failed"].([]any)[0].(map[string]any)["code"] != "not_found" {
			t.Fatalf("batch = %v", res)
		}
		if g := e.group("G2"); g["members"] != float64(3) {
			t.Fatalf("G2 = %v", g)
		}
		if c, items := e.auditCount("user.batch"); c != 1 || !strings.Contains(stringify(items[0]), g2) {
			t.Fatalf("batch audit = %d %v", c, items)
		}
		// Deleting a group moves its members to the default group and tells them.
		e.mustDo(e.admin, http.MethodDelete, "/api/admin/groups/"+g2, nil, 204)
		e.mustDo(e.admin, http.MethodDelete, "/api/admin/groups/"+g2, nil, 404)
		if g := e.group("默认"); g["members"] != float64(3) {
			t.Fatalf("default after delete = %v", g)
		}
		n := e.expectNotifs(e.carol, "account.group_changed", 3)
		if !strings.Contains(n[0]["body"].(string), "已被删除") {
			t.Fatalf("delete notification = %v", n[0])
		}
		if c, items := e.auditCount("group.delete"); c != 1 || !strings.Contains(stringify(items[0]), `"movedMembers":3`) {
			t.Fatalf("delete audit = %d %v", c, items)
		}
	})

	t.Run("audit", func(t *testing.T) {
		if c, _ := e.auditCount("group.create"); c != 2 {
			t.Fatalf("group.create = %d", c)
		}
		if c, items := e.auditCount("group.update"); c != 3 || !strings.Contains(stringify(items[0]), `"before"`) {
			t.Fatalf("group.update = %d %v", c, items)
		}
		// set_group in the batch: 3 more user.group_change entries.
		if c, _ := e.auditCount("user.group_change"); c != 4 {
			t.Fatalf("user.group_change = %d", c)
		}
	})
	_ = context.Background()
}

func TestGroupMultiplierAndSharing(t *testing.T) {
	e, bob := setupTiers(t)
	base := e.h.srv.URL
	carolID, bobID := e.userID(e.carol), e.userID(bob)
	plat, own, bobUp, adminShared := newModelUpstream(t, 0), newModelUpstream(t, 0), newModelUpstream(t, 0), newModelUpstream(t, 0)
	e.channel(e.admin, map[string]any{"name": "plat", "type": "openai", "baseUrl": pub(plat.srv.URL) + "/v1", "scope": "global", "models": models("m1")})
	e.channel(e.carol, map[string]any{"name": "carol-own", "type": "openai", "baseUrl": pub(own.srv.URL) + "/v1", "models": models("m2")})
	for _, m := range []string{"m1", "m2", "m3", "m4"} {
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": m, "inputPerM": "1000", "outputPerM": "1000",
			"cacheReadPerM": "100"}, 201)
	}
	platID := ""
	for _, it := range e.mustDo(e.admin, http.MethodGet, "/api/channels?q=plat", nil, 200)["items"].([]any) {
		platID = it.(map[string]any)["id"].(string)
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "cost", "model": "up-m1", "channelId": platID, "inputPerM": "100", "outputPerM": "100"}, 201)
	half := e.newGroup(map[string]any{"name": "Half", "priceMultiplier": "0.5"})
	e.moveUser(carolID, half)
	e.enforceBilling()
	e.credit(carolID, "10")
	_, carolKey := e.key(e.carol, map[string]any{"name": "ck"})
	call := func(model string) (int, map[string]any, string) {
		code, out, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", carolKey, chat(model)))
		e.app.FlushLogs(context.Background())
		return code, out, raw
	}

	// Platform request: 10 + 2 tokens at 1000/M = 0.012, × 0.5 = 0.006. The
	// cost price (0.0012) is not multiplied.
	if code, _, raw := call("m1"); code != 200 {
		t.Fatalf("platform = %d %s", code, raw)
	}
	l := e.lastLog(e.admin, "model=m1")
	if l["charge"] != "0.006" || l["priceMultiplier"] != "0.5" || l["cost"] != "0.0012" || l["channelTier"] != "platform" {
		t.Fatalf("platform log = %v", l)
	}
	if w := e.mustDo(e.carol, http.MethodGet, "/api/billing/wallet", nil, 200); w["balance"] != "9.994" {
		t.Fatalf("wallet = %v", w)
	}
	// Own channels stay free; no multiplier is logged.
	if code, _, raw := call("m2"); code != 200 {
		t.Fatalf("own = %d %s", code, raw)
	}
	if l := e.lastLog(e.admin, "model=m2"); l["charge"] != "0" || l["priceMultiplier"] != nil || l["channelTier"] != "own" {
		t.Fatalf("own log = %v", l)
	}

	t.Run("plan charge meter uses the multiplied price", func(t *testing.T) {
		plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
			"name": "C", "description": "", "duration": "30d", "models": []string{"m1"}, "stackable": false,
			"rules": []map[string]any{{"id": "c", "label": "c", "meter": "charge", "window": map[string]any{"kind": "lifetime"}, "limit": "1"}},
		}, 201)
		sub := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": carolID, "planId": plan["id"], "periods": 1}, 201)
		if code, _, raw := call("m1"); code != 200 {
			t.Fatalf("plan request = %d %s", code, raw)
		}
		if l := e.lastLog(e.admin, "model=m1"); l["quotaCharge"] != "0.006" || l["charge"] != "0" || l["priceMultiplier"] != "0.5" {
			t.Fatalf("plan log = %v", l)
		}
		e.expectUsed(e.carol, sub["id"].(string), map[string]string{"c": "0.006"})
		e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions/"+sub["id"].(string)+"/cancel", map[string]any{}, 200)
	})

	t.Run("plaza", func(t *testing.T) {
		mine := e.mustDo(e.carol, http.MethodGet, "/api/plaza/mine", nil, 200)
		var m1 map[string]any
		for _, it := range mine["items"].([]any) {
			if m := it.(map[string]any); m["model"] == "m1" {
				m1 = m
			}
		}
		p, bp := m1["price"].(map[string]any), m1["basePrice"].(map[string]any)
		if p["inputPerM"] != "500" || p["cacheReadPerM"] != "50" || bp["inputPerM"] != "1000" || m1["priceMultiplier"] != "0.5" ||
			p["currentMultiplier"] != "1" || p["schedule"] != nil || p["scheduleTimezone"] != "Asia/Shanghai" {
			t.Fatalf("plaza mine m1 = %v", m1)
		}
		pub := e.mustDo(e.carol, http.MethodGet, "/api/plaza/models", nil, 200)
		for _, it := range pub["items"].([]any) {
			if m := it.(map[string]any); m["model"] == "m1" && m["price"].(map[string]any)["inputPerM"] != "1000" {
				t.Fatalf("plaza models m1 = %v", m)
			}
		}
		// Users of the default group see ×1.
		mine = e.mustDo(e.admin, http.MethodGet, "/api/plaza/mine", nil, 200)
		for _, it := range mine["items"].([]any) {
			if m := it.(map[string]any); m["model"] == "m1" && (m["priceMultiplier"] != "1" || m["price"].(map[string]any)["inputPerM"] != "1000") {
				t.Fatalf("admin plaza m1 = %v", m)
			}
		}
	})

	t.Run("multiplier 0 makes platform channels free", func(t *testing.T) {
		e.patchGroup(half, map[string]any{"priceMultiplier": "0"})
		before := e.mustDo(e.carol, http.MethodGet, "/api/billing/wallet", nil, 200)["balance"]
		if code, _, raw := call("m1"); code != 200 {
			t.Fatalf("free platform = %d %s", code, raw)
		}
		if l := e.lastLog(e.admin, "model=m1"); l["charge"] != "0" || l["priceMultiplier"] != "0" || l["cost"] != "0.0012" {
			t.Fatalf("free log = %v", l)
		}
		if w := e.mustDo(e.carol, http.MethodGet, "/api/billing/wallet", nil, 200); w["balance"] != before {
			t.Fatalf("wallet changed: %v → %v", before, w["balance"])
		}
		e.patchGroup(half, map[string]any{"priceMultiplier": "0.5"})
	})

	t.Run("group sharing", func(t *testing.T) {
		// An administrator shares Bob's (a regular user's) channel with carol's
		// group: it is a shared (free) channel for carol. Only channels.manage
		// may share with groups (phase5-api.md §5.2).
		if code, out, _ := call("m3"); code != 404 || errCode(out) != "model_not_found" {
			t.Fatalf("before share = %d %v", code, out)
		}
		resp, out := bob.do(http.MethodPost, "/api/channels", map[string]any{"name": "bob-g", "type": "openai", "baseUrl": pub(bobUp.srv.URL) + "/v1",
			"scope": "shared", "apiKey": "sk-x-0123456789", "sharedWith": map[string]any{"users": []string{}, "groups": []string{half}}, "models": models("m3")})
		if resp.StatusCode != 422 || out["error"].(map[string]any)["details"].(map[string]any)["sharedWith.groups"] == nil {
			t.Fatalf("group share by a regular user = %d %v", resp.StatusCode, out)
		}
		sID := e.channel(bob, map[string]any{"name": "bob-g", "type": "openai", "baseUrl": pub(bobUp.srv.URL) + "/v1", "scope": "shared",
			"sharedWith": map[string]any{"users": []string{}}, "models": models("m3")})
		e.shareGroups(sID, half)
		ch := e.mustDo(bob, http.MethodGet, "/api/channels/"+sID, nil, 200)
		if sw := ch["sharedWith"].(map[string]any); len(sw["users"].([]any)) != 0 || sw["groups"].([]any)[0] != half {
			t.Fatalf("sharedWith = %v", ch["sharedWith"])
		}
		if code, _, raw := call("m3"); code != 200 || bobUp.hits.Load() != 1 {
			t.Fatalf("group share = %d %s", code, raw)
		}
		if l := e.lastLog(e.admin, "model=m3"); l["channelTier"] != "shared" || l["charge"] != "0" || l["priceMultiplier"] != nil {
			t.Fatalf("group share log = %v", l)
		}
		// Carol sees the channel (summary only); alice (other group) cannot use it.
		if v := e.mustDo(e.carol, http.MethodGet, "/api/channels/"+sID, nil, 200); v["baseUrl"] != nil || v["name"] != "bob-g" {
			t.Fatalf("carol view = %v", v)
		}
		found := false
		for _, it := range e.mustDo(e.carol, http.MethodGet, "/api/channels", nil, 200)["items"].([]any) {
			found = found || it.(map[string]any)["id"] == sID
		}
		if !found {
			t.Fatal("group-shared channel missing from carol's channel list")
		}
		mine := e.mustDo(e.carol, http.MethodGet, "/api/plaza/mine", nil, 200)
		for _, it := range mine["items"].([]any) {
			if m := it.(map[string]any); m["model"] == "m3" && (m["billing"] != "free" || m["sources"].(map[string]any)["shared"] != float64(1)) {
				t.Fatalf("plaza m3 = %v", m)
			}
		}
		// Leaving the group removes access.
		def := e.group("默认")["id"].(string)
		e.moveUser(carolID, def)
		if code, _, _ := call("m3"); code != 404 {
			t.Fatalf("after leaving the group = %d", code)
		}
		e.mustDo(e.carol, http.MethodGet, "/api/channels/"+sID, nil, 404)
		e.moveUser(carolID, half)
		// An administrator's channel shared with the group stays platform tier
		// (billed, multiplied).
		e.channel(e.admin, map[string]any{"name": "admin-g", "type": "openai", "baseUrl": pub(adminShared.srv.URL) + "/v1", "scope": "shared",
			"sharedWith": map[string]any{"groups": []string{half}}, "models": models("m4")})
		if code, _, raw := call("m4"); code != 200 || adminShared.hits.Load() != 1 {
			t.Fatalf("admin group share = %d %s", code, raw)
		}
		if l := e.lastLog(e.admin, "model=m4"); l["channelTier"] != "platform" || l["charge"] != "0.006" || l["priceMultiplier"] != "0.5" {
			t.Fatalf("admin group share log = %v", l)
		}
		// Array input is still accepted as a user list.
		id := e.channel(bob, map[string]any{"name": "bob-u", "type": "openai", "baseUrl": pub(bobUp.srv.URL) + "/v1", "scope": "shared",
			"sharedWith": []string{carolID}, "models": models("m5")})
		if sw := e.mustDo(bob, http.MethodGet, "/api/channels/"+id, nil, 200)["sharedWith"].(map[string]any); sw["users"].([]any)[0] != carolID ||
			len(sw["groups"].([]any)) != 0 {
			t.Fatalf("array sharedWith = %v", sw)
		}
		resp, out = e.admin.do(http.MethodPost, "/api/channels", map[string]any{"name": "bad", "type": "openai", "baseUrl": pub(bobUp.srv.URL) + "/v1",
			"scope": "shared", "apiKey": "sk-x-0123456789", "sharedWith": map[string]any{"groups": []string{"01920000-0000-7000-8000-00000000ffff"}},
			"models": models("m6")})
		if resp.StatusCode != 422 {
			t.Fatalf("unknown group share = %d %v", resp.StatusCode, out)
		}
		// Deleting the group drops its shares.
		e.mustDo(e.admin, http.MethodDelete, "/api/admin/groups/"+half, nil, 204)
		time.Sleep(50 * time.Millisecond)
		if sw := e.mustDo(bob, http.MethodGet, "/api/channels/"+sID, nil, 200)["sharedWith"].(map[string]any); len(sw["groups"].([]any)) != 0 {
			t.Fatalf("shares after group delete = %v", sw)
		}
		if code, _, _ := call("m3"); code != 404 {
			t.Fatalf("after group delete = %d", code)
		}
		_ = bobID
	})
}
