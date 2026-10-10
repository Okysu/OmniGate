package app_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Round 6 (continued), docs/contracts/phase7-api.md §3 and §4.4: quota reset
// and bulk extension of subscriptions.

// ruleUsage returns rule id → rule usage of subscription subID as seen by c.
func (e *gwEnv) ruleUsage(c *client, subID string) map[string]map[string]any {
	e.t.Helper()
	if err := e.app.FlushLogs(context.Background()); err != nil { // Record runs asynchronously
		e.t.Fatal(err)
	}
	subs := e.mustDo(c, http.MethodGet, "/api/billing/subscriptions", nil, 200)
	for _, it := range subs["items"].([]any) {
		s := it.(map[string]any)
		if s["id"] != subID {
			continue
		}
		out := map[string]map[string]any{}
		for _, r := range s["rules"].([]any) {
			rm := r.(map[string]any)
			out[rm["id"].(string)] = rm
		}
		return out
	}
	e.t.Fatalf("subscription %s not found", subID)
	return nil
}

func (e *gwEnv) expectUsed(c *client, subID string, want map[string]string) map[string]map[string]any {
	e.t.Helper()
	rules := e.ruleUsage(c, subID)
	for id, used := range want {
		if rules[id]["used"] != used {
			e.t.Fatalf("rule %s used = %v, want %s (%v)", id, rules[id]["used"], used, rules[id])
		}
	}
	return rules
}

func TestQuotaResetAndExtend(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	ctx := context.Background()
	up := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "plat", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("w1", "w2")})
	day := map[string]any{"kind": "calendar", "unit": "day"}
	w1 := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
		"name": "W1", "description": "", "duration": "30d", "models": []string{"w1"}, "stackable": true,
		"rules": []map[string]any{
			{"id": "cal", "meter": "requests", "window": day, "limit": "100"},
			{"id": "roll", "meter": "requests", "window": map[string]any{"kind": "rolling", "duration": "1h"}, "limit": "100"},
			{"id": "sess", "meter": "requests", "window": map[string]any{"kind": "session", "duration": "5h"}, "limit": "100"},
			{"id": "per", "meter": "requests", "window": map[string]any{"kind": "period", "every": "1d"}, "limit": "100"},
			{"id": "life", "label": "总量", "meter": "requests", "window": map[string]any{"kind": "lifetime"}, "limit": "100"},
		},
	}, 201)
	w2 := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
		"name": "W2", "description": "", "duration": "30d", "models": []string{"w2"}, "stackable": true,
		"rules": []map[string]any{{"id": "daily", "label": "每日", "meter": "requests", "window": day, "limit": "1"}},
	}, 201)
	aliceID, carolID := e.userID(e.admin), e.userID(e.carol)
	grant := func(user string, plan map[string]any) map[string]any {
		return e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": user, "planId": plan["id"], "periods": 1}, 201)
	}
	aliceW1, carolW1, carolW2 := grant(aliceID, w1)["id"].(string), grant(carolID, w1)["id"].(string), grant(carolID, w2)
	carolW2ID := carolW2["id"].(string)
	_, aliceKey := e.key(e.admin, map[string]any{"name": "a"})
	_, carolKey := e.key(e.carol, map[string]any{"name": "c"})
	call := func(key, model string) int {
		code, _, _ := readBody(gwPost(t, ctx, base, "/v1/chat/completions", key, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model)))
		e.app.FlushLogs(ctx)
		return code
	}
	reset := func(body map[string]any, query string, want int) map[string]any {
		t.Helper()
		if _, ok := body["note"]; !ok {
			body["note"] = ""
		}
		if _, ok := body["includeLifetime"]; !ok {
			body["includeLifetime"] = false
		}
		return e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions/reset-quota"+query, body, want)
	}
	byPlan := func(id any) map[string]any { return map[string]any{"planId": id, "status": "active"} }

	for i := 0; i < 2; i++ {
		if code := call(aliceKey, "w1"); code != 200 {
			t.Fatalf("w1 request = %d", code)
		}
	}
	all2 := map[string]string{"cal": "2", "roll": "2", "sess": "2", "per": "2", "life": "2"}
	before := e.expectUsed(e.admin, aliceW1, all2)
	if before["sess"]["windowStart"] == nil {
		t.Fatal("session window not started")
	}

	t.Run("dry run counts depend on every parameter", func(t *testing.T) {
		cases := []struct {
			body map[string]any
			want float64
		}{
			{map[string]any{"target": map[string]any{"ids": []string{aliceW1}}, "rules": nil}, 1},
			{map[string]any{"target": byPlan(w1["id"]), "rules": nil}, 2},
			{map[string]any{"target": byPlan(nil), "rules": nil}, 3},
			{map[string]any{"target": byPlan(nil), "rules": []string{"life"}}, 0},
			{map[string]any{"target": byPlan(nil), "rules": []string{"life"}, "includeLifetime": true}, 2},
			{map[string]any{"target": byPlan(nil), "rules": []string{"daily"}}, 1},
			{map[string]any{"target": map[string]any{"ids": []string{aliceW1, carolW2ID}}, "rules": []string{"cal"}}, 1},
		}
		for i, c := range cases {
			res := reset(c.body, "?dryRun=true", 200)
			if res["affected"] != c.want || len(res["subscriptions"].([]any)) != int(c.want) {
				t.Fatalf("case %d: %v", i, res)
			}
		}
		e.expectUsed(e.admin, aliceW1, all2)
		if n, _ := e.auditCount("subscription.quota_reset"); n != 0 {
			t.Fatalf("dry run audited: %d", n)
		}
	})

	t.Run("validation", func(t *testing.T) {
		out := reset(map[string]any{"target": map[string]any{"ids": []string{aliceW1}}, "rules": nil, "note": strings.Repeat("x", 201)}, "", 422)
		if out["error"].(map[string]any)["details"].(map[string]any)["note"] == nil {
			t.Fatalf("long note = %v", out)
		}
		reset(map[string]any{"rules": nil}, "", 422)
		reset(map[string]any{"target": map[string]any{"planId": nil, "status": "expired"}, "rules": nil}, "", 422)
		reset(map[string]any{"target": map[string]any{"ids": []string{}}, "rules": nil}, "", 422)
		reset(map[string]any{"target": map[string]any{"ids": []string{aliceW1}}, "rules": []string{}}, "", 422)
		reset(map[string]any{"target": byPlan("01a00000-0000-7000-8000-000000000000"), "rules": nil}, "", 404)
		e.mustDo(e.carol, http.MethodPost, "/api/admin/billing/subscriptions/reset-quota", map[string]any{"target": byPlan(nil), "rules": nil, "includeLifetime": false, "note": ""}, 403)
	})

	t.Run("reset clears the current window of each kind", func(t *testing.T) {
		res := reset(map[string]any{"target": map[string]any{"ids": []string{aliceW1}}, "rules": nil, "note": "维护补偿"}, "", 200)
		if res["affected"] != float64(1) || res["subscriptions"].([]any)[0] != aliceW1 {
			t.Fatalf("reset = %v", res)
		}
		rules := e.expectUsed(e.admin, aliceW1, map[string]string{"cal": "0", "roll": "0", "sess": "0", "per": "0", "life": "2"})
		// The session window restarted at the reset (phase11-api.md §1): empty,
		// refreshing 5 hours after the reset; rolling windows have no refresh time.
		anchor, _ := rules["sess"]["windowStart"].(string)
		start, err := time.Parse(time.RFC3339Nano, anchor)
		if err != nil || rules["sess"]["resetsAt"] != start.Add(5*time.Hour).Format(time.RFC3339) ||
			time.Since(start) > time.Minute || rules["roll"]["resetsAt"] != nil ||
			rules["cal"]["windowStart"] != before["cal"]["windowStart"] {
			t.Fatalf("after reset: sess=%v roll=%v cal=%v", rules["sess"], rules["roll"], rules["cal"])
		}
		if code := call(aliceKey, "w1"); code != 200 {
			t.Fatalf("w1 after reset = %d", code)
		}
		rules = e.expectUsed(e.admin, aliceW1, map[string]string{"cal": "1", "roll": "1", "sess": "1", "per": "1", "life": "3"})
		if rules["sess"]["windowStart"] != anchor {
			t.Fatalf("usage went to another session: %v (anchor %s)", rules["sess"], anchor)
		}
		// Lifetime only with includeLifetime.
		reset(map[string]any{"target": map[string]any{"ids": []string{aliceW1}}, "rules": []string{"life"}, "includeLifetime": true}, "", 200)
		e.expectUsed(e.admin, aliceW1, map[string]string{"cal": "1", "life": "0"})
		e.expectUsed(e.carol, carolW1, map[string]string{"cal": "0", "life": "0"})
	})

	t.Run("blocked users are admitted again immediately", func(t *testing.T) {
		if code := call(carolKey, "w2"); code != 200 {
			t.Fatalf("first w2 = %d", code)
		}
		if code := call(carolKey, "w2"); code != 429 {
			t.Fatalf("second w2 = %d", code)
		}
		res := reset(map[string]any{"target": byPlan(w2["id"]), "rules": nil, "note": "福利"}, "", 200)
		if res["affected"] != float64(1) {
			t.Fatalf("reset by plan = %v", res)
		}
		if code := call(carolKey, "w2"); code != 200 {
			t.Fatalf("w2 after reset = %d", code)
		}
	})

	t.Run("audit and notifications", func(t *testing.T) {
		n, items := e.auditCount("subscription.quota_reset")
		if n != 3 {
			t.Fatalf("audit entries = %d", n)
		}
		md := stringify(items[0].(map[string]any)["metadata"])
		if !strings.Contains(md, `"affected":1`) || !strings.Contains(md, `"note":"福利"`) || !strings.Contains(md, `"planId"`) {
			t.Fatalf("audit metadata = %s", md)
		}
		a := e.expectNotifs(e.admin, "subscription.quota_reset", 2)
		if !strings.Contains(a[1]["body"].(string), "维护补偿") || !strings.Contains(a[1]["body"].(string), "W1") {
			t.Fatalf("alice notification = %v", a[1])
		}
		c := e.expectNotifs(e.carol, "subscription.quota_reset", 1)
		if !strings.Contains(c[0]["body"].(string), "福利") || c[0]["data"].(map[string]any)["note"] != "福利" {
			t.Fatalf("carol notification = %v", c[0])
		}
	})

	t.Run("extend", func(t *testing.T) {
		ext := func(body map[string]any, query string, want int) map[string]any {
			t.Helper()
			if _, ok := body["note"]; !ok {
				body["note"] = ""
			}
			return e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions/extend"+query, body, want)
		}
		endsAt := func(c *client, id string) time.Time {
			t.Helper()
			subs := e.mustDo(c, http.MethodGet, "/api/billing/subscriptions", nil, 200)
			for _, it := range subs["items"].([]any) {
				if s := it.(map[string]any); s["id"] == id {
					ts, _ := time.Parse(time.RFC3339Nano, s["endsAt"].(string))
					return ts
				}
			}
			t.Fatalf("subscription %s not found", id)
			return time.Time{}
		}
		for _, d := range []string{"", "30m", "367d", "7"} {
			out := ext(map[string]any{"target": byPlan(nil), "duration": d}, "", 422)
			if out["error"].(map[string]any)["details"].(map[string]any)["duration"] == nil {
				t.Fatalf("duration %q = %v", d, out)
			}
		}
		was := endsAt(e.carol, carolW2ID)
		res := ext(map[string]any{"target": byPlan(w1["id"]), "duration": "7d"}, "?dryRun=true", 200)
		if res["affected"] != float64(2) {
			t.Fatalf("dry run = %v", res)
		}
		if !endsAt(e.carol, carolW1).Equal(endsAt(e.carol, carolW1)) || !endsAt(e.carol, carolW2ID).Equal(was) {
			t.Fatal("dry run changed endsAt")
		}
		res = ext(map[string]any{"target": map[string]any{"ids": []string{carolW2ID}}, "duration": "7d", "note": "抱歉"}, "", 200)
		if res["affected"] != float64(1) || res["subscriptions"].([]any)[0] != carolW2ID {
			t.Fatalf("extend = %v", res)
		}
		if got := endsAt(e.carol, carolW2ID); !got.Equal(was.Add(7 * 24 * time.Hour)) {
			t.Fatalf("endsAt = %s, want %s", got, was.Add(7*24*time.Hour))
		}
		if n, items := e.auditCount("subscription.extend"); n != 1 || !strings.Contains(stringify(items[0]), `"duration":"7d"`) {
			t.Fatalf("audit extend = %v", items)
		}
		c := e.expectNotifs(e.carol, "subscription.extended", 1)
		if !strings.Contains(c[0]["body"].(string), "抱歉") || c[0]["data"].(map[string]any)["duration"] != "7d" {
			t.Fatalf("extended notification = %v", c[0])
		}
		// Only active subscriptions are targeted.
		e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions/"+aliceW1+"/cancel", map[string]any{}, 200)
		if res := ext(map[string]any{"target": byPlan(w1["id"]), "duration": "1h"}, "?dryRun=true", 200); res["affected"] != float64(1) {
			t.Fatalf("after cancel = %v", res)
		}
		if res := reset(map[string]any{"target": map[string]any{"ids": []string{aliceW1}}, "rules": nil}, "?dryRun=true", 200); res["affected"] != float64(0) {
			t.Fatalf("reset of cancelled = %v", res)
		}
	})
}
