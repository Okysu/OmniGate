package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// acmeUpstream speaks the fictional Acme JSON Lines protocol of the editor's
// "自定义协议" template (docs/contracts/phase9-api.md §2).
type acmeUpstream struct {
	srv     *httptest.Server
	last    atomic.Value // map[string]any: last /v1/generate body
	lastHdr atomic.Value // http.Header
	hits    atomic.Int64
}

const acmeKey = "ak-acme-secret-0123"

func newAcmeUpstream(t *testing.T) *acmeUpstream {
	u := &acmeUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

func acmeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"code":%q,"message":%q}`, code, msg)
}

func (u *acmeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Acme-Key") != acmeKey {
		acmeError(w, http.StatusBadRequest, "invalid_key", "key revoked")
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"name":"acme-1"},{"name":"acme-2"}]}`)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/generate" {
		acmeError(w, http.StatusNotFound, "not_found", r.URL.Path)
		return
	}
	u.hits.Add(1)
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	u.last.Store(req)
	u.lastHdr.Store(r.Header.Clone())
	model, _ := req["model"].(string)
	stream, _ := req["stream"].(bool)
	input, _ := req["input"].([]any)
	lastText := ""
	if len(input) > 0 {
		lastText, _ = input[len(input)-1].(map[string]any)["text"].(string)
	}
	tools, _ := req["tools"].([]any)
	switch {
	case model == "flaky-1":
		acmeError(w, http.StatusBadRequest, "overloaded", "busy, try later")
		return
	case lastText == "BADREQ":
		acmeError(w, http.StatusBadRequest, "bad_request", "input rejected")
		return
	}
	flusher := w.(http.Flusher)
	if model == "slow-1" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		if lastText == "LOOP" {
			fmt.Fprint(w, "LOOP\n")
			flusher.Flush()
			return
		}
		for i := 0; i < 220; i++ {
			fmt.Fprint(w, "SPIN\n")
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(35 * time.Millisecond):
			}
		}
		return
	}
	stop := "end"
	callID, callName := "", ""
	if len(tools) > 0 {
		stop, callID = "call", "call_7"
		callName, _ = tools[0].(map[string]any)["name"].(string)
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		calls := []any{}
		if callID != "" {
			calls = append(calls, map[string]any{"id": callID, "name": callName, "args": `{"city":"北京"}`})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "r-1", "output": map[string]any{"text": "你好", "calls": calls}, "stop": stop,
			"usage": map[string]any{"in": 5, "out": 2}})
		return
	}
	lines := []string{`{"event":"think","text":"嗯"}`, `{"event":"text","text":"你"}`, `{"event":"text","text":"好！"}`}
	if callID != "" {
		lines = append(lines, fmt.Sprintf(`{"event":"call","id":%q,"name":%q,"args":"{\"city\":\"北京\"}"}`, callID, callName))
	}
	lines = append(lines, fmt.Sprintf(`{"event":"done","stop":%q,"usage":{"in":12,"out":9}}`, stop))
	payload := []byte(strings.Join(lines, "\n")) // no trailing newline: endStream flushes the last line
	w.Header().Set("Content-Type", "application/x-ndjson")
	// Small writes split lines and multi-byte characters across chunks.
	for i := 0; i < len(payload); i += 7 {
		_, _ = w.Write(payload[i:min(i+7, len(payload))])
		flusher.Flush()
		time.Sleep(time.Millisecond)
	}
}

// readSSE returns the raw text of a streamed response.
func readSSE(t *testing.T, resp *http.Response) (int, string) {
	t.Helper()
	defer resp.Body.Close()
	var sb strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		sb.WriteString(sc.Text())
		sb.WriteString("\n")
	}
	return resp.StatusCode, sb.String()
}

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s: missing %q in\n%s", what, w, got)
		}
	}
}

// publishPlugin saves files as the draft, publishes and approves it.
func (e *gwEnv) publishPlugin(pid string, files map[string]string, draftVersion int, approve bool) map[string]any {
	e.t.Helper()
	e.mustDo(e.admin, http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": files, "version": draftVersion}, 200)
	v := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/publish", nil, 201)
	if approve && v["approval"] == "pending" {
		v = e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/versions/"+v["id"].(string)+"/approve", map[string]any{"decision": "approve"}, 200)
	}
	return v
}

const slowSrc = `import { definePlugin } from "@omnigate/plugin-sdk"
export default definePlugin({
  buildRequest(req: any) {
    return {
      url: "/v1/generate",
      headers: { "X-Acme-Key": og.secret("apiKey") },
      body: { model: req.model, stream: !!req.stream, input: req.messages.map((m: any) => ({ role: m.role, text: typeof m.content === "string" ? m.content : "" })) },
    }
  },
  parseResponse(res: any) { return { choices: [{ message: { content: res.body } }] } },
  parseStream(chunk: Uint8Array, state: any) {
    if (!state.d) state.d = new TextDecoder()
    const text = state.d.decode(chunk, { stream: true })
    if (text.indexOf("LOOP") >= 0) { for (;;) {} }
    if (text.indexOf("SPIN") >= 0) { const t = Date.now(); while (Date.now() - t < 30) {} }
    return [{ type: "delta", content: "." }]
  },
})`

func slowFiles() map[string]string {
	return map[string]string{
		"manifest.json": `{"id":"acme.slow","name":"慢插件","version":"0.1.0","sdk":1,"protocol":"custom","entry":"src/index.ts",
		  "permissions":{"network":["$baseUrl"],"secrets":["apiKey"],"schedule":[]},"capabilities":{},
		  "hooks":["buildRequest","parseResponse","parseStream"],"uiContributions":[]}`,
		"src/index.ts": slowSrc,
	}
}

func TestCustomProtocolChannel(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newAcmeUpstream(t)

	// Manifest validation through the editor.
	pl := e.mustDo(e.admin, http.MethodPost, "/api/plugins", map[string]any{"id": "acme.llm", "name": "Acme", "template": "custom-protocol"}, 201)
	pid := pl["id"].(string)
	draft := e.mustDo(e.admin, http.MethodGet, "/api/plugins/"+pid+"/draft", nil, 200)
	files := map[string]string{}
	for k, v := range draft["files"].(map[string]any) {
		files[k] = v.(string)
	}
	bad := map[string]string{}
	for k, v := range files {
		bad[k] = v
	}
	bad["manifest.json"] = strings.Replace(files["manifest.json"], `"protocol": "custom"`, `"protocol": "custom", "extends": "openai.chat"`, 1)
	bad["manifest.json"] = strings.Replace(bad["manifest.json"], `"parseStream",`, ``, 1)
	e.mustDo(e.admin, http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": bad, "version": draft["version"]}, 200)
	build := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/build", nil, 200)
	mustContain(t, "invalid manifest", fmt.Sprint(build["diagnostics"]), "互斥", "parseStream")
	// Declared hooks must be exported.
	bad["manifest.json"] = files["manifest.json"]
	bad["src/index.ts"] = strings.Replace(files["src/index.ts"], "parseStream(chunk", "parseStreamX(chunk", 1)
	e.mustDo(e.admin, http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": bad, "version": 2}, 200)
	build = e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/build", nil, 200)
	if build["ok"] != false {
		t.Fatalf("missing export accepted: %v", build)
	}
	mustContain(t, "missing export", fmt.Sprint(build["diagnostics"]), "parseStream")

	// The template builds and its tests (including the streaming case) pass.
	e.mustDo(e.admin, http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": files, "version": 3}, 200)
	if b := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/build", nil, 200); b["ok"] != true || b["manifest"].(map[string]any)["protocol"] != "custom" {
		t.Fatalf("template build = %v", b)
	}
	for name := range files {
		if strings.HasPrefix(name, "tests/") {
			if r := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/test", map[string]any{"case": name}, 200); r["ok"] != true {
				t.Errorf("template test %s = %v", name, r)
			}
		}
	}
	// Streaming cases report each call and the Chat chunks a client would get.
	st := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/test", map[string]any{"case": "tests/parse-stream.json"}, 200)
	if calls := st["calls"].([]any); len(calls) != 5 || calls[4].(map[string]any)["hook"] != "endStream" || calls[4].(map[string]any)["chunk"] != nil ||
		calls[0].(map[string]any)["chunk"] != 0.0 || !strings.Contains(fmt.Sprint(st["chatChunks"]), "finish_reason:tool_calls") {
		t.Fatalf("stream test = %v", st)
	}
	// An inline streaming case with a failing expectation.
	r := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/test", map[string]any{"hook": "parseStream",
		"chunks": []string{`{"event":"text","text":"a"}` + "\n"}, "expect": map[string]any{"output": []any{map[string]any{"type": "delta", "content": "b"}}}}, 200)
	if r["ok"] != false || !strings.Contains(fmt.Sprint(r["expectation"]), "/0/content") {
		t.Fatalf("stream expectation = %v", r)
	}

	// The first version needs approval; pending versions can't back channels.
	v1 := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/publish", nil, 201)
	if v1["approval"] != "pending" {
		t.Fatalf("v1 = %v", v1)
	}
	v1ID := v1["id"].(string)
	if resp, out := e.admin.do(http.MethodPost, "/api/channels", map[string]any{"name": "acme", "pluginVersionId": v1ID, "baseUrl": up.srv.URL, "apiKey": acmeKey}); resp.StatusCode != 409 {
		t.Fatalf("pending custom version usable: %d %v", resp.StatusCode, out)
	}
	e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/versions/"+v1ID+"/approve", map[string]any{"decision": "approve"}, 200)
	if p := e.mustDo(e.admin, http.MethodGet, "/api/plugins/"+pid, nil, 200); p["protocol"] != "custom" || fmt.Sprint(p["kind"]) != "[channel]" {
		t.Fatalf("plugin summary = %v", p)
	}
	if resp, out := e.admin.do(http.MethodPost, "/api/channels", map[string]any{"name": "x", "type": "custom", "baseUrl": up.srv.URL, "apiKey": "k",
		"models": models("x")}); resp.StatusCode != 422 || !strings.Contains(fmt.Sprint(out), "pluginVersionId") {
		t.Fatalf("custom type without plugin = %d %v", resp.StatusCode, out)
	}
	chID := e.channel(e.admin, map[string]any{"name": "acme", "pluginVersionId": v1ID, "baseUrl": up.srv.URL, "apiKey": acmeKey,
		"models": []map[string]string{{"model": "acme", "upstreamModel": "acme-1"}, {"model": "slow", "upstreamModel": "acme-1"},
			{"model": "flaky", "upstreamModel": "acme-1"}}})
	ch := e.mustDo(e.admin, http.MethodGet, "/api/channels/"+chID, nil, 200)
	if ch["type"] != "custom" {
		t.Fatalf("channel type = %v", ch["type"])
	}
	// Connectivity test and model discovery go through the plugin's capabilities.
	if res := e.mustDo(e.admin, http.MethodPost, "/api/channels/"+chID+"/test", nil, 200); res["ok"] != true {
		t.Fatalf("channel test = %v", res)
	}
	if res := e.mustDo(e.admin, http.MethodPost, "/api/channels/"+chID+"/discover-models", nil, 200); !strings.Contains(fmt.Sprint(res), "acme-2") {
		t.Fatalf("discover = %v", res)
	}
	_, key := e.key(e.admin, map[string]any{"name": "k"})
	ctx := context.Background()

	// Chat unary.
	code, body, raw := readBody(gwPost(t, ctx, base, "/v1/chat/completions", key, `{"model":"acme","max_tokens":64,"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`))
	if code != 200 || body["object"] != "chat.completion" || body["model"] != "acme" || !strings.Contains(raw, `"content":"你好"`) ||
		!strings.Contains(raw, `"prompt_tokens":5`) {
		t.Fatalf("chat unary = %d %s", code, raw)
	}
	sent := up.last.Load().(map[string]any)
	hdr := up.lastHdr.Load().(http.Header)
	if sent["model"] != "acme-1" || sent["maxTokens"] != 64.0 || sent["stream"] != false || hdr.Get("X-Acme-Key") != acmeKey || hdr.Get("Authorization") != "" {
		t.Fatalf("upstream request = %v %v", sent, hdr)
	}
	log := e.lastLog(e.admin, "model=acme")
	if u := log["usage"].(map[string]any); u["input"] != 5.0 || u["output"] != 2.0 || u["estimated"] != false {
		t.Fatalf("unary usage = %v", log["usage"])
	}

	// Chat stream with usage and tool calls.
	tools := `"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]`
	code, out := readSSE(t, gwPost(t, ctx, base, "/v1/chat/completions", key,
		`{"model":"acme","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"天气"}],`+tools+`}`))
	if code != 200 {
		t.Fatalf("chat stream = %d %s", code, out)
	}
	mustContain(t, "chat stream", out, `"role":"assistant"`, `"reasoning_content":"嗯"`, `"content":"你"`, `"content":"好！"`, `"name":"get_weather"`,
		`\"city\":\"北京\"`, `"finish_reason":"tool_calls"`, `"prompt_tokens":12`, "data: [DONE]")
	log = e.lastLog(e.admin, "model=acme")
	if u := log["usage"].(map[string]any); u["input"] != 12.0 || u["output"] != 9.0 || u["estimated"] != false || log["stream"] != true {
		t.Fatalf("stream log = %v", log)
	}

	// Anthropic client, streaming with a tool.
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(`{"model":"acme","max_tokens":100,"stream":true,
		"messages":[{"role":"user","content":"天气"}],"tools":[{"name":"get_weather","input_schema":{"type":"object"}}]}`))
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	code, out = readSSE(t, resp)
	if code != 200 {
		t.Fatalf("anthropic stream = %d %s", code, out)
	}
	mustContain(t, "anthropic stream", out, "event: message_start", `"thinking":"嗯"`, `"text":"你"`, `"type":"tool_use"`, `"name":"get_weather"`,
		`"stop_reason":"tool_use"`, `"output_tokens":9`, "event: message_stop")
	if sent := up.last.Load().(map[string]any); sent["maxTokens"] != 100.0 || sent["stream"] != true {
		t.Fatalf("converted request = %v", sent)
	}

	// Responses client: unary and streaming.
	code, _, raw = readBody(gwPost(t, ctx, base, "/v1/responses", key, `{"model":"acme","input":"hi"}`))
	if code != 200 || !strings.Contains(raw, `"object":"response"`) || !strings.Contains(raw, `"text":"你好"`) || !strings.Contains(raw, `"input_tokens":5`) {
		t.Fatalf("responses unary = %d %s", code, raw)
	}
	code, out = readSSE(t, gwPost(t, ctx, base, "/v1/responses", key, `{"model":"acme","input":"hi","stream":true}`))
	if code != 200 {
		t.Fatalf("responses stream = %d %s", code, out)
	}
	mustContain(t, "responses stream", out, "response.created", "response.output_text.delta", "response.completed", `"output_tokens":9`)

	// Custom channels do not serve embeddings.
	if code, _, raw := readBody(gwPost(t, ctx, base, "/v1/embeddings", key, `{"model":"acme","input":"x"}`)); code != 404 {
		t.Fatalf("embeddings on custom channel = %d %s", code, raw)
	}

	// normalizeError: Acme's "overloaded" (HTTP 400) maps to 503, so the
	// request is retried on the next channel; "bad_request" stays a 400.
	flaky := e.channel(e.admin, map[string]any{"name": "acme-flaky", "pluginVersionId": v1ID, "baseUrl": up.srv.URL, "apiKey": acmeKey, "priority": 10,
		"models": []map[string]string{{"model": "flaky", "upstreamModel": "flaky-1"}}})
	code, _, raw = readBody(gwPost(t, ctx, base, "/v1/chat/completions", key, `{"model":"flaky","messages":[{"role":"user","content":"hi"}]}`))
	if code != 200 {
		t.Fatalf("flaky = %d %s", code, raw)
	}
	log = e.lastLog(e.admin, "model=flaky")
	atts := log["fallbackPath"].([]any)
	if len(atts) != 2 || atts[0].(map[string]any)["channelId"] != flaky || atts[0].(map[string]any)["errorClass"] != "upstream_unavailable" {
		t.Fatalf("flaky attempts = %v", atts)
	}
	code, body, raw = readBody(gwPost(t, ctx, base, "/v1/chat/completions", key, `{"model":"acme","messages":[{"role":"user","content":"BADREQ"}]}`))
	if code != 400 || !strings.Contains(raw, "bad_request: input rejected") {
		t.Fatalf("bad request = %d %v", code, body)
	}
	// A rejected key (Acme 400 invalid_key) is an upstream authentication error.
	e.mustDo(e.admin, http.MethodDelete, "/api/channels/"+flaky, nil, 204)
	cur := e.mustDo(e.admin, http.MethodGet, "/api/channels/"+chID, nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+chID, map[string]any{"apiKey": "ak-wrong-key", "version": cur["version"]}, 200)
	time.Sleep(80 * time.Millisecond)
	if code, _, raw := readBody(gwPost(t, ctx, base, "/v1/chat/completions", key, `{"model":"acme","messages":[{"role":"user","content":"hi"}]}`)); code != 502 ||
		!strings.Contains(raw, "upstream_authentication") {
		t.Fatalf("invalid key = %d %s", code, raw)
	}
	cur = e.mustDo(e.admin, http.MethodGet, "/api/channels/"+chID, nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+chID, map[string]any{"apiKey": acmeKey, "version": cur["version"]}, 200)

	// Per-call timeout before the first byte: plugin_error, retried on the
	// next channel. Total JS time over 5 s mid-stream: in-band plugin_error.
	sp := e.mustDo(e.admin, http.MethodPost, "/api/plugins", map[string]any{"id": "acme.slow", "name": "慢插件", "template": "blank"}, 201)
	sv := e.publishPlugin(sp["id"].(string), slowFiles(), 1, true)
	slow := e.channel(e.admin, map[string]any{"name": "slow", "pluginVersionId": sv["id"], "baseUrl": up.srv.URL, "apiKey": acmeKey, "priority": 10,
		"models": []map[string]string{{"model": "slow", "upstreamModel": "slow-1"}}})
	code, out = readSSE(t, gwPost(t, ctx, base, "/v1/chat/completions", key, `{"model":"slow","stream":true,"messages":[{"role":"user","content":"LOOP"}]}`))
	if code != 200 || !strings.Contains(out, `"content":"好！"`) {
		t.Fatalf("loop fallback = %d %s", code, out)
	}
	log = e.lastLog(e.admin, "model=slow")
	atts = log["fallbackPath"].([]any)
	if len(atts) != 2 || atts[0].(map[string]any)["channelId"] != slow || atts[0].(map[string]any)["errorClass"] != "plugin_error" {
		t.Fatalf("loop attempts = %v", atts)
	}
	start := time.Now()
	code, out = readSSE(t, gwPost(t, ctx, base, "/v1/chat/completions", key, `{"model":"slow","stream":true,"messages":[{"role":"user","content":"SPIN"}]}`))
	if code != 200 || !strings.Contains(out, `"content":"."`) || !strings.Contains(out, "plugin_error") || !strings.Contains(out, "总执行时间") {
		t.Fatalf("budget = %d %s", code, out)
	}
	if d := time.Since(start); d < 5*time.Second || d > 15*time.Second {
		t.Fatalf("budget interrupt after %s", d)
	}
	log = e.lastLog(e.admin, "model=slow")
	if log["errorClass"] != "plugin_error" || log["attempts"] != 1.0 {
		t.Fatalf("budget log = %v", log)
	}
}

// TestCustomProtocolFirstVersionApproval: switching a plugin to the custom
// protocol needs approval even when its permissions don't change.
func TestCustomProtocolFirstVersionApproval(t *testing.T) {
	e := setupGateway(t)
	pl := e.mustDo(e.admin, http.MethodPost, "/api/plugins", map[string]any{"id": "acme.switch", "name": "切换", "template": "blank"}, 201)
	pid := pl["id"].(string)
	d := e.mustDo(e.admin, http.MethodGet, "/api/plugins/"+pid+"/draft", nil, 200)
	files := map[string]string{}
	for k, v := range d["files"].(map[string]any) {
		files[k] = v.(string)
	}
	v1 := e.publishPlugin(pid, files, int(d["version"].(float64)), true)
	if v1["approval"] != "approved" {
		t.Fatalf("v1 = %v", v1)
	}
	// Same permissions, inheriting protocol: auto-approved.
	files["manifest.json"] = strings.Replace(files["manifest.json"], `"0.1.0"`, `"0.2.0"`, 1)
	if v := e.publishPlugin(pid, files, 0, false); v["approval"] != "approved" {
		t.Fatalf("v2 = %v", v)
	}
	// Same permissions, custom protocol: pending.
	custom := slowFiles()
	custom["manifest.json"] = strings.NewReplacer(`"acme.slow"`, `"acme.switch"`, `"0.1.0"`, `"0.3.0"`).Replace(custom["manifest.json"])
	v3 := e.publishPlugin(pid, custom, 0, false)
	if v3["approval"] != "pending" || len(v3["permissionDiff"].(map[string]any)["added"].([]any)) != 0 {
		t.Fatalf("v3 = %v", v3)
	}
	// Later custom versions with unchanged permissions are auto-approved again.
	e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/versions/"+v3["id"].(string)+"/approve", map[string]any{"decision": "approve"}, 200)
	custom["manifest.json"] = strings.Replace(custom["manifest.json"], `"0.3.0"`, `"0.4.0"`, 1)
	if v := e.publishPlugin(pid, custom, 0, false); v["approval"] != "approved" {
		t.Fatalf("v4 = %v", v)
	}
	_ = io.Discard
}
