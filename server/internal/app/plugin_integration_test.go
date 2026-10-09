package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pluginUpstream emulates DeepSeek management endpoints plus a chat endpoint
// that records what the gateway sent.
type pluginUpstream struct {
	srv      *httptest.Server
	lastReq  atomic.Value // map[string]any
	lastHdr  atomic.Value // http.Header
	lastPath atomic.Value
}

func newPluginUpstream(t *testing.T) *pluginUpstream {
	u := &pluginUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user/balance":
			if r.Header.Get("Authorization") != "Bearer sk-deepseek-123456" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"88.80","granted_balance":"8.80","topped_up_balance":"80.00"}]}`)
		case "/models", "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`)
		default:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			u.lastReq.Store(body)
			u.lastHdr.Store(r.Header.Clone())
			u.lastPath.Store(r.URL.Path)
			fmt.Fprint(w, `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (e *gwEnv) mustDo(c *client, method, path string, body any, want int) map[string]any {
	e.t.Helper()
	resp, out := c.do(method, path, body)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s = %d (want %d): %v", method, path, resp.StatusCode, want, out)
	}
	return out
}

func pluginByKey(t *testing.T, list map[string]any, key string) map[string]any {
	for _, it := range list["items"].([]any) {
		if m := it.(map[string]any); m["key"] == key {
			return m
		}
	}
	t.Fatalf("plugin %s not listed", key)
	return nil
}

const signerSrc = `import { definePlugin } from "@omnigate/plugin-sdk"
export default definePlugin({
  capabilities: {
    "custom.spin"() { while (true) {} },
  },
  transformRequest(req: any, ctx: any) {
    req.body.tenant = ctx.config.tenant
    req.path = "/custom" + req.path
    return req
  },
  signRequest(req: any) {
    if (req.body.messages?.[0]?.content === "boom") throw new Error("refusing to sign")
    req.headers["X-Custom-Key"] = og.secret("apiKey")
    return req
  },%s
})`

func signerFiles(version, migrate string) map[string]string {
	manifest := fmt.Sprintf(`{"id":"acme.signer","name":"自定义签名","version":%q,"sdk":1,"extends":"openai.chat","entry":"src/index.ts",
	  "defaults":{"models":[{"model":"signed","upstreamModel":"up-signed"}]},
	  "configSchema":{"type":"object","properties":{"tenant":{"type":"string","default":"t-default"},"region":{"type":"string"}}},
	  "permissions":{"network":["$baseUrl"],"secrets":["apiKey"],"schedule":[]},
	  "capabilities":{"custom.spin":{"output":"json","userTriggerable":true,"timeout":"1s"}},
	  "hooks":["transformRequest","signRequest"],"uiContributions":[]}`, version)
	return map[string]string{
		"manifest.json": manifest,
		"src/index.ts":  fmt.Sprintf(signerSrc, migrate),
		"tests/sign.json": `{"hook":"signRequest","secrets":{"apiKey":"k1"},
		  "request":{"dialect":"openai.chat","path":"/chat/completions","headers":{},"body":{"messages":[{"role":"user","content":"hi"}]}},
		  "expect":{"output":{"headers":{"X-Custom-Key":"k1"}}}}`,
	}
}

func TestPluginLifecycle(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newPluginUpstream(t)

	// Built-in and bundled plugins are installed and approved.
	list := e.mustDo(e.admin, http.MethodGet, "/api/plugins", nil, 200)
	ds := pluginByKey(t, list, "community.deepseek")
	pluginByKey(t, list, "builtin.openai")
	if ds["latest"] == nil || ds["latest"].(map[string]any)["approval"] != "approved" || ds["extends"] != "openai.chat" {
		t.Fatalf("deepseek = %v", ds)
	}
	dsVersion := ds["latest"].(map[string]any)["id"].(string)
	if resp, _ := e.carol.do(http.MethodPost, "/api/plugins", map[string]any{"id": "x.y", "name": "x"}); resp.StatusCode != 403 {
		t.Fatalf("plain user created plugin: %d", resp.StatusCode)
	}

	// Channel pinned to the DeepSeek plugin; config is validated against its schema.
	resp, out := e.admin.do(http.MethodPost, "/api/channels", map[string]any{"name": "ds", "pluginVersionId": dsVersion,
		"baseUrl": up.srv.URL + "/v1", "apiKey": "sk-deepseek-123456", "pluginConfig": map[string]any{"currency": "EUR"}})
	if resp.StatusCode != 422 || !strings.Contains(fmt.Sprint(out), "pluginConfig.currency") {
		t.Fatalf("bad config = %d %v", resp.StatusCode, out)
	}
	ch := e.mustDo(e.admin, http.MethodPost, "/api/channels", map[string]any{"name": "ds", "pluginVersionId": dsVersion,
		"baseUrl": up.srv.URL + "/v1", "apiKey": "sk-deepseek-123456"}, 201)
	chID := ch["id"].(string)
	if ch["type"] != "openai" || ch["plugin"].(map[string]any)["key"] != "community.deepseek" || len(ch["models"].([]any)) != 2 ||
		ch["pluginConfig"].(map[string]any)["currency"] != "CNY" {
		t.Fatalf("channel = %v", ch)
	}
	res := e.mustDo(e.admin, http.MethodPost, "/api/channels/"+chID+"/capabilities/balance.get", nil, 200)
	if res["ok"] != true || res["output"].(map[string]any)["total"] != "88.80" {
		t.Fatalf("balance = %v", res)
	}
	res = e.mustDo(e.admin, http.MethodPost, "/api/channels/"+chID+"/capabilities/usage.query", nil, 200)
	if res["unsupported"] != true {
		t.Fatalf("usage should be unsupported: %v", res)
	}
	caps := e.mustDo(e.admin, http.MethodGet, "/api/channels/"+chID+"/capabilities", nil, 200)
	if len(caps["capabilities"].([]any)) != 4 || caps["results"].(map[string]any)["balance.get"] == nil || len(caps["ui"].([]any)) != 3 {
		t.Fatalf("capabilities = %v", caps)
	}
	listCh := e.mustDo(e.admin, http.MethodGet, "/api/channels", nil, 200)
	badges := listCh["items"].([]any)[0].(map[string]any)["badges"].([]any)
	if badges[0].(map[string]any)["value"] != "88.80" || badges[0].(map[string]any)["currency"] != "CNY" {
		t.Fatalf("badges = %v", badges)
	}

	// Editor workflow: create → draft → build → test → publish → approve.
	pl := e.mustDo(e.admin, http.MethodPost, "/api/plugins", map[string]any{"id": "acme.signer", "name": "签名", "template": "blank"}, 201)
	pid := pl["id"].(string)
	e.mustDo(e.admin, http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": signerFiles("0.1.0", ""), "version": 1}, 200)
	build := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/build", nil, 200)
	if build["ok"] != true {
		t.Fatalf("build = %v", build)
	}
	tr := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/test", map[string]any{"case": "tests/sign.json"}, 200)
	if tr["ok"] != true {
		t.Fatalf("test = %v", tr)
	}
	v1 := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/publish", nil, 201)
	if v1["approval"] != "pending" || len(v1["permissionDiff"].(map[string]any)["added"].([]any)) == 0 {
		t.Fatalf("v1 = %v", v1)
	}
	v1ID := v1["id"].(string)
	resp, out = e.admin.do(http.MethodPost, "/api/channels", map[string]any{"name": "signed", "pluginVersionId": v1ID, "baseUrl": up.srv.URL, "apiKey": "sk-custom-999"})
	if resp.StatusCode != 409 || out["error"].(map[string]any)["code"] != "plugin_not_approved" {
		t.Fatalf("pending version usable: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.carol.do(http.MethodPost, "/api/plugins/"+pid+"/versions/"+v1ID+"/approve", map[string]any{"decision": "approve"}); resp.StatusCode != 403 {
		t.Fatal("plain user approved a plugin")
	}
	e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/versions/"+v1ID+"/approve", map[string]any{"decision": "approve", "note": "reviewed"}, 200)
	sch := e.mustDo(e.admin, http.MethodPost, "/api/channels", map[string]any{"name": "signed", "pluginVersionId": v1ID, "baseUrl": up.srv.URL,
		"apiKey": "sk-custom-999", "pluginConfig": map[string]any{"tenant": "t-1", "region": "old"}}, 201)
	schID := sch["id"].(string)
	time.Sleep(80 * time.Millisecond)
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	call := func(content string) (int, string) {
		code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key,
			fmt.Sprintf(`{"model":"signed","messages":[{"role":"user","content":%q}]}`, content)))
		return code, raw
	}
	if code, raw := call("hi"); code != 200 {
		t.Fatalf("signed call = %d %s", code, raw)
	}
	hdr := up.lastHdr.Load().(http.Header)
	body := up.lastReq.Load().(map[string]any)
	if hdr.Get("X-Custom-Key") != "sk-custom-999" || hdr.Get("Authorization") != "" || body["tenant"] != "t-1" ||
		up.lastPath.Load() != "/custom/chat/completions" || body["model"] != "up-signed" {
		t.Fatalf("hooks not applied: path=%v auth=%q custom=%q body=%v", up.lastPath.Load(), hdr.Get("Authorization"), hdr.Get("X-Custom-Key"), body)
	}
	if code, raw := call("boom"); code != 502 || !strings.Contains(raw, "plugin_error") || strings.Contains(raw, "sk-custom") {
		t.Fatalf("hook failure = %d %s", code, raw)
	}

	// v0.2.0 with identical permissions is auto-approved; upgrading migrates config.
	migrate := `
  migrateConfig(old: any, from: string) { return { tenant: old.tenant + "@" + from } },`
	e.mustDo(e.admin, http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": signerFiles("0.2.0", migrate), "version": 0}, 200)
	v2 := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/publish", nil, 201)
	if v2["approval"] != "approved" {
		t.Fatalf("v2 should be auto-approved: %v", v2)
	}
	resp, out = e.admin.do(http.MethodPost, "/api/plugins/"+pid+"/draft/publish", nil)
	if resp.StatusCode != 404 && resp.StatusCode != 409 {
		t.Fatalf("republish without draft = %d %v", resp.StatusCode, out)
	}
	cur := e.mustDo(e.admin, http.MethodGet, "/api/channels/"+schID, nil, 200)
	up2 := e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+schID, map[string]any{"pluginVersionId": v2["id"], "version": cur["version"]}, 200)
	if up2["pluginConfig"].(map[string]any)["tenant"] != "t-1@0.1.0" || up2["plugin"].(map[string]any)["version"] != "0.2.0" {
		t.Fatalf("upgrade = %v", up2)
	}
	rb := e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+schID, map[string]any{"pluginVersionId": v1ID, "version": up2["version"]}, 200)
	if rb["plugin"].(map[string]any)["version"] != "0.1.0" {
		t.Fatalf("rollback = %v", rb)
	}

	// Disabling a plugin removes its channels from routing.
	p := e.mustDo(e.admin, http.MethodGet, "/api/plugins/"+pid, nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/plugins/"+pid, map[string]any{"status": "disabled", "version": p["version"]}, 200)
	time.Sleep(80 * time.Millisecond)
	if code, _ := call("hi"); code != 404 {
		t.Fatalf("disabled plugin still routable: %d", code)
	}
	p = e.mustDo(e.admin, http.MethodGet, "/api/plugins/"+pid, nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/plugins/"+pid, map[string]any{"status": "enabled", "version": p["version"]}, 200)
	time.Sleep(80 * time.Millisecond)
	if code, raw := call("hi"); code != 200 {
		t.Fatalf("re-enabled = %d %s", code, raw)
	}

	// Repeated resource violations auto-disable the plugin.
	for i := 0; i < 3; i++ {
		r := e.mustDo(e.admin, http.MethodPost, "/api/channels/"+schID+"/capabilities/custom.spin", nil, 200)
		if r["ok"] != false || !strings.Contains(fmt.Sprint(r["error"]), "超时") {
			t.Fatalf("spin %d = %v", i, r)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		p = e.mustDo(e.admin, http.MethodGet, "/api/plugins/"+pid, nil, 200)
		if p["status"] == "disabled" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if p["status"] != "disabled" {
		t.Fatalf("plugin not auto-disabled: %v", p["status"])
	}

	// Export → import round trip creates a draft for the existing plugin.
	req, _ := http.NewRequest(http.MethodGet, base+"/api/plugins/"+pid+"/versions/"+v2["id"].(string)+"/export", nil)
	zresp, err := e.admin.c.Do(req)
	if err != nil || zresp.StatusCode != 200 || zresp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("export = %v %v", err, zresp)
	}
	zip, _ := io.ReadAll(zresp.Body)
	zresp.Body.Close()
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	fw, _ := mw.CreateFormFile("file", "p.zip")
	_, _ = fw.Write(zip)
	_ = mw.Close()
	ireq, _ := http.NewRequest(http.MethodPost, base+"/api/plugins/import", &form)
	ireq.Header.Set("Content-Type", mw.FormDataContentType())
	ireq.Header.Set("X-Requested-With", "XMLHttpRequest")
	iresp, err := e.admin.c.Do(ireq)
	if err != nil || iresp.StatusCode != 201 {
		b, _ := io.ReadAll(iresp.Body)
		t.Fatalf("import = %v %d %s", err, iresp.StatusCode, b)
	}
	var imported map[string]any
	_ = json.NewDecoder(iresp.Body).Decode(&imported)
	iresp.Body.Close()
	if imported["plugin"].(map[string]any)["id"] != pid || imported["build"].(map[string]any)["ok"] != true {
		t.Fatalf("import result = %v", imported)
	}

	// Deleting a plugin that channels still use is refused; bundled plugins can't be deleted.
	if resp, out := e.admin.do(http.MethodDelete, "/api/plugins/"+pid, nil); resp.StatusCode != 409 || out["error"].(map[string]any)["code"] != "plugin_in_use" {
		t.Fatalf("delete in-use plugin = %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.admin.do(http.MethodDelete, "/api/plugins/"+ds["id"].(string), nil); resp.StatusCode != 409 {
		t.Fatalf("delete bundled plugin = %d", resp.StatusCode)
	}
	e.mustDo(e.admin, http.MethodDelete, "/api/channels/"+schID, nil, 204)
	e.mustDo(e.admin, http.MethodDelete, "/api/plugins/"+pid, nil, 204)
	if resp, _ := e.admin.do(http.MethodGet, "/api/plugins/"+pid, nil); resp.StatusCode != 404 {
		t.Fatal("deleted plugin still visible")
	}

	// Audit trail.
	logs := e.mustDo(e.admin, http.MethodGet, "/api/admin/audit-logs?pageSize=200", nil, 200)
	seen := map[string]bool{}
	for _, it := range logs["items"].([]any) {
		seen[it.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"plugin.create", "plugin.publish", "plugin.approve", "plugin.update", "plugin.auto_disabled", "plugin.import",
		"channel.plugin_upgrade", "capability.invoke", "plugin.delete"} {
		if !seen[a] {
			t.Errorf("audit missing %s", a)
		}
	}
	_ = chID
}
