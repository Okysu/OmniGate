package app_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// metricValue reads a counter of the default registry (0 when absent).
func metricValue(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			for k, v := range labels {
				if got[k] != v {
					continue next
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

func meterFiles(version, half string) map[string]string {
	return map[string]string{
		"manifest.json": fmt.Sprintf(`{"id":"acme.meters","name":"测试计量","version":%q,"sdk":1,"kind":["billing"],"entry":"src/index.ts",
		  "permissions":{"network":[],"secrets":[],"schedule":[]},"capabilities":{},"hooks":[],"uiContributions":[],
		  "billing":{"meters":{"half":{"label":"半个"},"boom":{"label":"抛错"},"spin":{"label":"超时"},"probe":{"label":"上下文"}}}}`, version),
		"src/index.ts": fmt.Sprintf(`import { definePlugin } from "@omnigate/plugin-sdk"
export default definePlugin({
  billing: {
    meters: {
      half: { computeUnits() { return %q } },
      boom: { computeUnits() { throw new Error("meter exploded") } },
      spin: { computeUnits() { for (;;) {} } },
      probe: {
        computeUnits(usage: any, ctx: any) {
          const pure = typeof og.fetch === "undefined" && typeof og.storage === "undefined" && typeof og.secret === "undefined"
          const ok = pure && ctx.model === "m1" && ctx.servedModel === "m1" && ctx.inbound === "openai.chat" &&
            ctx.channelTier === "platform" && ctx.channelId.length === 36 && ctx.imageCount === 0 && usage.output === 2
          return ok ? 1 : 0
        },
      },
    },
  },
})`, half),
	}
}

func ruleUsed(t *testing.T, e *gwEnv) map[string]string {
	t.Helper()
	if err := e.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	subs := e.mustDo(e.admin, http.MethodGet, "/api/billing/subscriptions", nil, 200)
	out := map[string]string{}
	for _, r := range subs["items"].([]any)[0].(map[string]any)["rules"].([]any) {
		m := r.(map[string]any)
		out[m["id"].(string)] = m["used"].(string)
	}
	return out
}

func TestBillingPluginMeters(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "oa", "type": "openai", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})

	meters := e.mustDo(e.admin, http.MethodGet, "/api/admin/billing/meters", nil, 200)
	mustContain(t, "meters", fmt.Sprint(meters["items"]), "custom:community.billing-examples.weighted_tokens",
		"custom:community.billing-examples.per_image", "tokens.total")
	bundled := pluginByKey(t, e.mustDo(e.admin, http.MethodGet, "/api/plugins", nil, 200), "community.billing-examples")
	if fmt.Sprint(bundled["kind"]) != "[billing]" || len(bundled["meters"].([]any)) != 2 {
		t.Fatalf("bundled billing plugin = %v", bundled)
	}

	// Editor: a billing plugin with a meter test case.
	pl := e.mustDo(e.admin, http.MethodPost, "/api/plugins", map[string]any{"id": "acme.meters", "name": "测试计量", "template": "blank"}, 201)
	pid := pl["id"].(string)
	e.mustDo(e.admin, http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": meterFiles("0.1.0", "0.5"), "version": 1}, 200)
	if r := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/test", map[string]any{"meter": "half", "expect": map[string]any{"output": "0.5"}}, 200); r["ok"] != true {
		t.Fatalf("meter test = %v", r)
	}
	if r := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/test", map[string]any{"meter": "boom"}, 200); r["ok"] != false ||
		!strings.Contains(fmt.Sprint(r["error"]), "meter exploded") {
		t.Fatalf("throwing meter test = %v", r)
	}
	rule := func(id, meter, limit string) map[string]any {
		return map[string]any{"id": id, "meter": meter, "window": map[string]any{"kind": "lifetime"}, "limit": limit}
	}
	planBody := func(rules ...map[string]any) map[string]any {
		return map[string]any{"name": "插件计量", "description": "", "duration": "30d", "models": []string{"m1"}, "stackable": false, "rules": rules}
	}
	planErr := func(rules ...map[string]any) string {
		t.Helper()
		resp, out := e.admin.do(http.MethodPost, "/api/admin/billing/plans", planBody(rules...))
		if resp.StatusCode != 422 {
			t.Fatalf("plan accepted: %d %v", resp.StatusCode, out)
		}
		return fmt.Sprint(out["error"].(map[string]any)["details"])
	}
	// Rule validation at save time.
	v1 := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/publish", nil, 201)
	mustContain(t, "pending version", planErr(rule("a", "custom:acme.meters.half", "10")), "rules[0].meter", "没有已批准")
	e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/versions/"+v1["id"].(string)+"/approve", map[string]any{"decision": "approve"}, 200)
	mustContain(t, "unknown plugin", planErr(rule("a", "custom:nope.plugin.half", "10")), "不存在")
	mustContain(t, "unknown meter", planErr(rule("a", "custom:acme.meters.missing", "10")), "没有已批准且声明了计量 missing")
	mustContain(t, "bad syntax", planErr(rule("a", "custom:Acme", "10")), "custom:<插件 ID>.<计量名>")

	plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", planBody(
		rule("weighted", "custom:community.billing-examples.weighted_tokens", "100000"),
		rule("half", "custom:acme.meters.half", "10.5"),
		rule("boom", "custom:acme.meters.boom", "10"),
		rule("spin", "custom:acme.meters.spin", "10"),
		rule("probe", "custom:acme.meters.probe", "10"),
	), 201)
	for _, r := range plan["rules"].([]any) {
		m := r.(map[string]any)
		if m["pluginVersionId"] == nil || (m["id"] == "half" && (m["pluginVersionId"] != v1["id"] || m["meterLabel"] != "半个")) ||
			(m["id"] == "weighted" && (m["meterLabel"] != "加权 token（输出 ×4）" || m["meterUnit"] != "token")) {
			t.Fatalf("rule not pinned: %v", m)
		}
	}
	// Clients can't set the snapshot fields; built-in meters have none.
	p0 := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", planBody(map[string]any{"id": "r", "meter": "requests",
		"window": map[string]any{"kind": "lifetime"}, "limit": "5", "meterLabel": "伪造", "pluginVersionId": v1["id"]}), 201)
	if r0 := p0["rules"].([]any)[0].(map[string]any); r0["meterLabel"] != nil || r0["pluginVersionId"] != nil {
		t.Fatalf("built-in rule snapshot = %v", r0)
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": e.userID(e.admin), "planId": plan["id"], "periods": 1}, 201)
	// User-facing pages see the meter labels in the snapshot.
	subsView := e.mustDo(e.admin, http.MethodGet, "/api/billing/subscriptions", nil, 200)
	mustContain(t, "subscription rules", fmt.Sprint(subsView["items"]), "meterLabel:半个", "meterUnit:token")
	catalog := e.mustDo(e.carol, http.MethodGet, "/api/plans", nil, 200)
	mustContain(t, "catalog", fmt.Sprint(catalog), "meterLabel:加权 token（输出 ×4）")
	_, key := e.key(e.admin, map[string]any{"name": "k"})
	call := func() {
		t.Helper()
		if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, chatBody)); code != 200 {
			t.Fatalf("call = %d %s", code, raw)
		}
	}
	boom0 := metricValue(t, "omnigate_billing_plugin_errors_total", map[string]string{"plugin": "acme.meters", "meter": "boom", "reason": "exception"})
	spin0 := metricValue(t, "omnigate_billing_plugin_errors_total", map[string]string{"plugin": "acme.meters", "meter": "spin", "reason": "timeout"})
	call()
	// prompt 10 + completion 2 × 4 = 18; throwing and looping meters count 0.
	used := ruleUsed(t, e)
	if used["weighted"] != "18" || used["half"] != "0.5" || used["boom"] != "0" || used["spin"] != "0" || used["probe"] != "1" {
		t.Fatalf("used = %v", used)
	}
	if b := metricValue(t, "omnigate_billing_plugin_errors_total", map[string]string{"plugin": "acme.meters", "meter": "boom", "reason": "exception"}); b != boom0+1 {
		t.Fatalf("boom metric = %v (before %v)", b, boom0)
	}
	if s := metricValue(t, "omnigate_billing_plugin_errors_total", map[string]string{"plugin": "acme.meters", "meter": "spin", "reason": "timeout"}); s != spin0+1 {
		t.Fatalf("spin metric = %v (before %v)", s, spin0)
	}

	// Upgrading the plugin keeps the subscription's snapshot (pinned version).
	v2 := e.publishPlugin(pid, meterFiles("0.2.0", "2"), 0, false)
	if v2["approval"] != "approved" {
		t.Fatalf("v2 = %v", v2)
	}
	call()
	if used := ruleUsed(t, e); used["half"] != "1" || used["weighted"] != "36" || used["probe"] != "2" {
		t.Fatalf("after upgrade = %v", used)
	}
	// A plan saved now pins the new version.
	p2 := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", planBody(rule("half", "custom:acme.meters.half", "10")), 201)
	if p2["rules"].([]any)[0].(map[string]any)["pluginVersionId"] != v2["id"] {
		t.Fatalf("new plan pin = %v", p2["rules"])
	}

	// A disabled plugin counts 0 (with an alert log and metric) and its
	// meters can't be saved into rules.
	p := e.mustDo(e.admin, http.MethodGet, "/api/plugins/"+pid, nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/plugins/"+pid, map[string]any{"status": "disabled", "version": p["version"]}, 200)
	time.Sleep(50 * time.Millisecond)
	un0 := metricValue(t, "omnigate_billing_plugin_errors_total", map[string]string{"plugin": "acme.meters", "meter": "half", "reason": "unavailable"})
	call()
	if used := ruleUsed(t, e); used["half"] != "1" || used["weighted"] != "54" || used["probe"] != "2" {
		t.Fatalf("disabled plugin = %v", used)
	}
	if u := metricValue(t, "omnigate_billing_plugin_errors_total", map[string]string{"plugin": "acme.meters", "meter": "half", "reason": "unavailable"}); u != un0+1 {
		t.Fatalf("unavailable metric = %v (before %v)", u, un0)
	}
	resp, out := e.admin.do(http.MethodPatch, "/api/admin/billing/plans/"+p2["id"].(string), map[string]any{"version": p2["version"],
		"rules": []map[string]any{rule("half", "custom:acme.meters.half", "10")}})
	if resp.StatusCode != 422 || !strings.Contains(fmt.Sprint(out), "已停用") {
		t.Fatalf("save with disabled plugin = %d %v", resp.StatusCode, out)
	}
	// Other edits keep the stored pins.
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/billing/plans/"+p2["id"].(string), map[string]any{"version": p2["version"], "name": "改名"}, 200)
}
