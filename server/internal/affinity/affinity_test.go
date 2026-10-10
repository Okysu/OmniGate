package affinity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// newAPIJSON is new-api's channel affinity setting with its two original
// rules, as an administrator would paste it.
const newAPIJSON = `{
  "enabled": true,
  "switch_on_success": true,
  "max_entries": 100000,
  "default_ttl_seconds": 3600,
  "rules": [
    {
      "name": "codex cli trace",
      "model_regex": ["^gpt-.*$"],
      "path_regex": ["/v1/responses"],
      "key_sources": [{"type": "gjson", "path": "prompt_cache_key"}],
      "value_regex": "",
      "ttl_seconds": 0,
      "param_override_template": {"operations": [{"mode": "pass_headers", "value": ["Originator", "Session_id", "Thread_id", "Session-Id",
        "Thread-Id", "X-Client-Request-Id", "User-Agent", "X-Codex-Beta-Features", "X-Codex-Turn-State", "X-Codex-Turn-Metadata",
        "X-Codex-Window-Id", "X-Codex-Parent-Thread-Id", "X-OpenAI-Subagent", "X-OpenAI-Memgen-Request",
        "X-ResponsesAPI-Include-Timing-Metrics", "X-OpenAI-Internal-Codex-Responses-Lite"], "keep_origin": true}]},
      "skip_retry_on_failure": true,
      "include_using_group": true,
      "include_rule_name": true,
      "user_agent_include": null
    },
    {
      "name": "claude cli trace",
      "model_regex": ["^claude-.*$"],
      "path_regex": ["/v1/messages"],
      "key_sources": [{"type": "gjson", "path": "metadata.user_id"}],
      "value_regex": "",
      "ttl_seconds": 0,
      "param_override_template": {"operations": [{"mode": "pass_headers", "value": ["X-Stainless-Arch", "X-Stainless-Lang", "X-Stainless-Os",
        "X-Stainless-Package-Version", "X-Stainless-Retry-Count", "X-Stainless-Runtime", "X-Stainless-Runtime-Version",
        "X-Stainless-Timeout", "User-Agent", "X-App", "Anthropic-Beta", "Anthropic-Dangerous-Direct-Browser-Access",
        "Anthropic-Version", "X-Claude-Code-Session-Id"], "keep_origin": true}]},
      "skip_retry_on_failure": true,
      "include_using_group": true,
      "include_rule_name": true
    }
  ]
}`

func mustDecode(t *testing.T, raw string) Config {
	t.Helper()
	c, msg := Decode(json.RawMessage(raw))
	if msg != "" {
		t.Fatalf("Decode: %s", msg)
	}
	return c
}

func TestDecodeNewAPITemplate(t *testing.T) {
	c := mustDecode(t, newAPIJSON)
	if !c.Enabled || !c.SwitchOnSuccess || c.KeepOnChannelDisabled || c.MaxEntries != 100000 || c.DefaultTTLSeconds != 3600 || c.SessionMode != ModePrefer {
		t.Fatalf("globals = %+v", c)
	}
	if len(c.Rules) != 2 || c.Rules[0].Name != "codex cli trace" || c.Rules[1].Name != "claude cli trace" {
		t.Fatalf("rules = %+v", c.Rules)
	}
	for _, r := range c.Rules {
		// Legacy form: empty session_mode + skip_retry_on_failure = strict.
		if r.Mode(c.SessionMode) != ModeStrict {
			t.Errorf("%s: mode = %s", r.Name, r.Mode(c.SessionMode))
		}
		if p := r.Pass(); p == nil || !p.KeepOrigin || len(p.Names) < 10 {
			t.Errorf("%s: pass = %+v", r.Name, p)
		}
		if r.UserAgentInclude == nil || r.ModelRegex == nil {
			t.Errorf("%s: canonical slices must not be null", r.Name)
		}
	}
	// The canonical form round-trips.
	b, _ := json.Marshal(c)
	again := mustDecode(t, string(b))
	b2, _ := json.Marshal(again)
	if string(b) != string(b2) {
		t.Fatalf("not canonical:\n%s\n%s", b, b2)
	}
}

func TestDefault(t *testing.T) {
	c := Default()
	if !c.Enabled || c.SessionMode != ModePrefer || len(c.Rules) != 3 {
		t.Fatalf("default = %+v", c)
	}
	if c.Rules[0].Name != "gpt session" || c.Rules[1].Name != "codex cli trace" || c.Rules[2].Name != "claude cli trace" {
		t.Fatalf("preset order = %s, %s, %s", c.Rules[0].Name, c.Rules[1].Name, c.Rules[2].Name)
	}
	for _, r := range c.Rules {
		if r.Mode(c.SessionMode) != ModePrefer || !r.IncludeUsingGroup || !r.IncludeRuleName || r.IncludeModelName {
			t.Errorf("%s: %+v", r.Name, r)
		}
	}
	gpt := c.Rules[0]
	var sources []string
	for _, ks := range gpt.KeySources {
		sources = append(sources, ks.Type+":"+ks.Key+ks.Path)
	}
	if strings.Join(sources, ",") != "gjson:prompt_cache_key,request_header:Session_id,request_header:Session-Id,gjson:metadata.user_id,"+
		"request_header:X-Claude-Code-Session-Id,gjson:user,anchor:" || !gpt.InjectPromptCacheKey || gpt.InjectSessionHeader != "Session_id" ||
		strings.Join(gpt.ModelRegex, ",") != "^gpt-" || len(gpt.PathRegex) != 0 {
		t.Errorf("gpt session = %+v (%v)", gpt, sources)
	}
	if got := gpt.Pass().Names; strings.Join(got, ",") != strings.Join(codexHeaders, ",") {
		t.Errorf("gpt session headers = %v", got)
	}
	if got := c.Rules[1].Pass().Names; len(got) != 16 || got[1] != "Session_id" {
		t.Errorf("codex headers = %v", got)
	}
	if got := c.Rules[2].Pass().Names; len(got) != 14 || got[len(got)-1] != "X-Claude-Code-Session-Id" {
		t.Errorf("claude headers = %v", got)
	}
	for _, r := range c.Rules[1:] {
		if r.InjectPromptCacheKey || r.InjectSessionHeader != "" {
			t.Errorf("%s must not inject", r.Name)
		}
	}
}

func TestDecodeExtensions(t *testing.T) {
	// new-api documents have none of the extensions: they decode to off.
	for _, r := range mustDecode(t, newAPIJSON).Rules {
		if r.InjectPromptCacheKey || r.InjectSessionHeader != "" {
			t.Errorf("%s: extensions on by default", r.Name)
		}
	}
	c := mustDecode(t, `{"rules":[{"name":"a","key_sources":[{"type":"gjson","path":"user"},{"type":"anchor"}],
		"inject_prompt_cache_key":true,"inject_session_header":" Session_id "}]}`)
	r := c.Rules[0]
	if r.KeySources[1] != (KeySource{Type: SourceAnchor}) || !r.InjectPromptCacheKey || r.InjectSessionHeader != "Session_id" {
		t.Fatalf("rule = %+v", r)
	}
	// The canonical form carries the new fields and round-trips.
	b, _ := json.Marshal(c)
	if !strings.Contains(string(b), `{"type":"anchor"}`) || !strings.Contains(string(b), `"inject_prompt_cache_key":true`) ||
		!strings.Contains(string(b), `"inject_session_header":"Session_id"`) {
		t.Fatalf("canonical = %s", b)
	}
	b2, _ := json.Marshal(mustDecode(t, string(b)))
	if string(b) != string(b2) {
		t.Fatalf("not canonical:\n%s\n%s", b, b2)
	}
	b, _ = json.Marshal(Default())
	b2, _ = json.Marshal(mustDecode(t, string(b)))
	if string(b) != string(b2) {
		t.Fatalf("default not canonical:\n%s\n%s", b, b2)
	}
	for name, tc := range map[string]struct{ raw, want string }{
		"anchor key":       {`{"rules":[{"name":"r","key_sources":[{"type":"anchor","key":"x"}]}]}`, "rules[0].key_sources[0]：anchor 来源不接受 key 或 path"},
		"anchor path":      {`{"rules":[{"name":"r","key_sources":[{"type":"anchor","path":"messages"}]}]}`, "anchor 来源不接受"},
		"bad inject hdr":   {`{"rules":[{"name":"r","key_sources":[{"type":"anchor"}],"inject_session_header":"a b"}]}`, "rules[0].inject_session_header：请求头名称格式无效"},
		"forbidden inject": {`{"rules":[{"name":"r","key_sources":[{"type":"anchor"}],"inject_session_header":"Authorization"}]}`, "inject_session_header：请求头 \"Authorization\" 由网关管理"},
		"inject type":      {`{"rules":[{"name":"r","key_sources":[{"type":"anchor"}],"inject_prompt_cache_key":"yes"}]}`, "rules[0]：格式错误"},
	} {
		if _, msg := Decode(json.RawMessage(tc.raw)); !strings.Contains(msg, tc.want) {
			t.Errorf("%s: message %q does not contain %q", name, msg, tc.want)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	rule := func(extra string) string {
		return `{"rules":[{"name":"r","key_sources":[{"type":"gjson","path":"prompt_cache_key"}]` + extra + `}]}`
	}
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf(`{"name":"r%d","key_sources":[{"type":"gjson","path":"a"}]}`, i)
	}
	headers := make([]string, 65)
	for i := range headers {
		headers[i] = fmt.Sprintf(`"X-H%d"`, i)
	}
	for name, tc := range map[string]struct{ raw, want string }{
		"not object":     {`[]`, "JSON 对象"},
		"context_int":    {`{"rules":[{"name":"r","key_sources":[{"type":"context_int","key":"user_id"}]}]}`, "rules[0].key_sources[0].type：context_int 是 new-api 内部上下文类型"},
		"context_string": {`{"rules":[{"name":"r","key_sources":[{"type":"context_string","key":"x"}]}]}`, "context_string"},
		"unknown type":   {`{"rules":[{"name":"r","key_sources":[{"type":"cookie","key":"x"}]}]}`, "只能是 gjson、request_header 或 anchor"},
		"no sources":     {`{"rules":[{"name":"r"}]}`, "至少需要一个 Key 来源"},
		"gjson no path":  {`{"rules":[{"name":"r","key_sources":[{"type":"gjson"}]}]}`, "需要 path"},
		"bad header key": {`{"rules":[{"name":"r","key_sources":[{"type":"request_header","key":"a b"}]}]}`, "合法的请求头名称"},
		"no name":        {`{"rules":[{"name":" ","key_sources":[{"type":"gjson","path":"a"}]}]}`, "rules[0].name"},
		"dup names":      {`{"rules":[` + strings.Join([]string{many[0], many[0]}, ",") + `]}`, "重复"},
		"too many rules": {`{"rules":[` + strings.Join(many, ",") + `]}`, "最多 50 条规则"},
		"bad model re":   {rule(`,"model_regex":["(("]`), "rules[0].model_regex：正则"},
		"bad path re":    {rule(`,"path_regex":["[a"]`), "rules[0].path_regex"},
		"bad value re":   {rule(`,"value_regex":"*x"`), "rules[0].value_regex"},
		"bad mode":       {rule(`,"session_mode":"always"`), "rules[0].session_mode"},
		"global mode":    {`{"session_mode":"inherit"}`, "session_mode：只能是 off、prefer、strict"},
		"ttl":            {rule(`,"ttl_seconds":-1`), "ttl_seconds"},
		"max entries":    {`{"max_entries":0}`, "max_entries"},
		"set op": {rule(`,"param_override_template":{"operations":[{"mode":"set","path":"temperature","value":0.1}]}`),
			"rules[0].param_override_template.operations[0].mode：不支持 \"set\""},
		"legacy override": {rule(`,"param_override_template":{"temperature":0.2}`), "param_override_template.temperature：不支持"},
		"authorization":   {rule(`,"param_override_template":{"operations":[{"mode":"pass_headers","value":["Authorization"]}]}`), "\"Authorization\" 不允许透传"},
		"cookie":          {rule(`,"param_override_template":{"operations":[{"mode":"pass_headers","value":["X-A","cookie"]}]}`), "\"cookie\" 不允许透传"},
		"x-api-key":       {rule(`,"param_override_template":{"operations":[{"mode":"pass_headers","value":"X-Api-Key"}]}`), "不允许透传"},
		"host":            {rule(`,"param_override_template":{"operations":[{"mode":"pass_headers","value":["Host"]}]}`), "不允许透传"},
		"bad header":      {rule(`,"param_override_template":{"operations":[{"mode":"pass_headers","value":["X A"]}]}`), "格式无效"},
		"no headers":      {rule(`,"param_override_template":{"operations":[{"mode":"pass_headers","value":[]}]}`), "至少需要一个请求头"},
		"too many hdrs":   {rule(`,"param_override_template":{"operations":[{"mode":"pass_headers","value":[` + strings.Join(headers, ",") + `]}]}`), "最多 64 个请求头"},
		"conditions":      {rule(`,"param_override_template":{"operations":[{"mode":"pass_headers","value":["X-A"],"conditions":[{"path":"a"}]}]}`), "conditions：pass_headers 不支持该字段"},
	} {
		if _, msg := Decode(json.RawMessage(tc.raw)); !strings.Contains(msg, tc.want) {
			t.Errorf("%s: message %q does not contain %q", name, msg, tc.want)
		}
	}
	// A comma-separated value and an empty template are accepted.
	c := mustDecode(t, rule(`,"param_override_template":{"operations":[{"mode":"pass_headers","value":"X-A, x-a ,Session_id"}]}`))
	if got := c.Rules[0].Pass().Names; strings.Join(got, ",") != "X-A,Session_id" {
		t.Errorf("names = %v", got)
	}
	if c := mustDecode(t, rule(`,"param_override_template":{}`)); c.Rules[0].Pass() != nil {
		t.Error("empty template must pass nothing")
	}
}

func TestModeResolution(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		skip   bool
		global string
		want   string
	}{
		{"", false, ModePrefer, ModePrefer},
		{"", true, ModePrefer, ModeStrict},
		{"", false, ModeOff, ModeOff},
		{ModeInherit, true, ModeOff, ModeOff},
		{ModeOff, true, ModeStrict, ModeOff},
		{ModePrefer, true, ModeStrict, ModePrefer},
		{ModeStrict, false, ModeOff, ModeStrict},
	} {
		r := Rule{SessionMode: tc.mode, SkipRetryOnFailure: tc.skip}
		if got := r.Mode(tc.global); got != tc.want {
			t.Errorf("Mode(%q, skip=%v, global %s) = %s, want %s", tc.mode, tc.skip, tc.global, got, tc.want)
		}
	}
}

func TestMatch(t *testing.T) {
	c := mustDecode(t, `{"rules":[
	  {"name":"ua","model_regex":["^gpt-"],"path_regex":["^/v1/responses$"],"user_agent_include":["Codex"],
	   "key_sources":[{"type":"request_header","key":"X-Missing"},{"type":"gjson","path":"prompt_cache_key"},{"type":"request_header","key":"Session_id"}]},
	  {"name":"value","model_regex":[".*"],"key_sources":[{"type":"gjson","path":"metadata.user_id"}],"value_regex":"^user_[a-z0-9]+$"},
	  {"name":"fallback","key_sources":[{"type":"request_header","key":"Session_id"}]}
	]}`)
	h := http.Header{}
	h.Set("Session_id", "hdr-session")
	body := []byte(`{"model":"gpt-5","prompt_cache_key":"  conv-1  ","metadata":{"user_id":"user_abc"}}`)
	req := Request{Model: "gpt-5", Path: "/v1/responses", UserAgent: "codex_cli_rs/0.50 (Mac) CODEX", Header: h, Body: body}
	check := func(name string, req Request, rule, value string) {
		t.Helper()
		m := c.Match(req)
		switch {
		case rule == "" && m != nil:
			t.Errorf("%s: matched %s", name, m.Rule.Name)
		case rule != "" && (m == nil || m.Rule.Name != rule || m.Value != value):
			t.Errorf("%s: got %+v, want %s=%q", name, m, rule, value)
		}
	}
	// First non-empty key source, trimmed.
	check("ua", req, "ua", "conv-1")
	// User agent mismatch (case-insensitive include) → next rule.
	r2 := req
	r2.UserAgent = "curl/8"
	check("ua mismatch", r2, "value", "user_abc")
	// Model and path regexes.
	r3 := req
	r3.Model = "claude-x"
	check("model", r3, "value", "user_abc")
	r4 := req
	r4.Path = "/v1/responses/compact"
	check("path", r4, "value", "user_abc")
	// Key absent → falls through: body without prompt_cache_key uses the header.
	r5 := req
	r5.Body = []byte(`{"model":"gpt-5"}`)
	check("header source", r5, "ua", "hdr-session")
	// value_regex must match, else the next rule.
	r6 := r2
	r6.Body = []byte(`{"metadata":{"user_id":"User ABC"}}`)
	check("value regex", r6, "fallback", "hdr-session")
	// Multipart (no body) and no header: nothing applies.
	r7 := Request{Model: "gpt-5", Path: "/v1/images/edits", Header: http.Header{}}
	check("none", r7, "", "")
	// JSON null and empty values are absent.
	r8 := r2
	r8.Header, r8.Body = http.Header{}, []byte(`{"metadata":{"user_id":null}}`)
	check("null", r8, "", "")
	// Disabled: nothing applies.
	c.Enabled = false
	check("disabled", req, "", "")
}

func TestBindingKey(t *testing.T) {
	u1, u2, g1, g2 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	rule := &Rule{Name: "r"}
	key := func(r *Rule, user uuid.UUID, group *uuid.UUID, model, value string) [32]byte {
		return (&Match{Rule: r, Value: value}).bindingKey(Request{UserID: user, GroupID: group, Model: model})
	}
	base := key(rule, u1, &g1, "m1", "v")
	if key(rule, u2, &g1, "m1", "v") == base {
		t.Error("users must never share a binding")
	}
	if key(rule, u1, &g1, "m1", "w") == base {
		t.Error("different values must differ")
	}
	// Without include flags, group, model and rule name are not part of the key.
	if key(rule, u1, &g2, "m2", "v") != base || key(&Rule{Name: "other"}, u1, nil, "m3", "v") != base {
		t.Error("excluded parts changed the key")
	}
	withGroup := &Rule{Name: "r", IncludeUsingGroup: true}
	if key(withGroup, u1, &g1, "m", "v") == key(withGroup, u1, &g2, "m", "v") || key(withGroup, u1, nil, "m", "v") == key(withGroup, u1, &g1, "m", "v") {
		t.Error("include_using_group ignored")
	}
	withModel := &Rule{Name: "r", IncludeModelName: true}
	if key(withModel, u1, nil, "m1", "v") == key(withModel, u1, nil, "m2", "v") {
		t.Error("include_model_name ignored")
	}
	withName := &Rule{Name: "a", IncludeRuleName: true}
	if key(withName, u1, nil, "m", "v") == key(&Rule{Name: "b", IncludeRuleName: true}, u1, nil, "m", "v") {
		t.Error("include_rule_name ignored")
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func k(n int) (out [32]byte) {
	out[0], out[1] = byte(n), byte(n>>8)
	return out
}

func get(s *Store, n int) (uuid.UUID, bool) { return s.Get(k(n)) }

func TestStoreLRUAndTTL(t *testing.T) {
	clk := &clock{t: time.Unix(1_000_000, 0)}
	s := NewStore(clk.now)
	ch := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	s.Set(k(1), ch[0], "a", time.Minute, 3)
	s.Set(k(2), ch[1], "a", time.Minute, 3)
	s.Set(k(3), ch[2], "b", time.Minute, 3)
	if got, ok := get(s, 1); !ok || got != ch[0] { // 1 becomes most recently used
		t.Fatalf("get 1 = %v %v", got, ok)
	}
	s.Set(k(4), ch[3], "b", time.Minute, 3) // evicts 2, the least recently used
	if _, ok := get(s, 2); ok {
		t.Fatal("LRU entry not evicted")
	}
	if n, per := s.Stats(); n != 3 || per["a"] != 1 || per["b"] != 2 {
		t.Fatalf("stats = %d %v", n, per)
	}
	// Sliding TTL: a hit at 50s extends key 1 to 110s; key 3 expires at 60s.
	clk.add(50 * time.Second)
	if _, ok := get(s, 1); !ok {
		t.Fatal("key 1 expired early")
	}
	clk.add(30 * time.Second) // t = 80s
	if _, ok := get(s, 3); ok {
		t.Fatal("key 3 must have expired")
	}
	if _, ok := get(s, 1); !ok {
		t.Fatal("hit did not refresh key 1")
	}
	// Rebinding refreshes too and replaces the channel.
	clk.add(55 * time.Second) // t = 135s: key 1 refreshed at 80s lives until 140s
	s.Set(k(1), ch[2], "a", time.Minute, 3)
	clk.add(59 * time.Second)
	if got, ok := get(s, 1); !ok || got != ch[2] {
		t.Fatalf("rebound key 1 = %v %v", got, ok)
	}
	// Stats purge expired entries (key 4 set at 0s).
	if n, _ := s.Stats(); n != 1 {
		t.Fatalf("live entries = %d", n)
	}
	// A smaller capacity applies on the next write.
	for i := 10; i < 20; i++ {
		s.Set(k(i), ch[0], "c", time.Hour, 5)
	}
	if n, per := s.Stats(); n != 5 || per["c"] != 5 {
		t.Fatalf("capacity: %d %v", n, per)
	}
	if n := s.Clear("c"); n != 5 {
		t.Fatalf("clear rule = %d", n)
	}
	s.Set(k(30), ch[0], "d", time.Hour, 5)
	if n := s.Clear(""); n != 1 {
		t.Fatalf("clear all = %d", n)
	}
}

// newTestService serves the setting cfg with the test clock.
func newTestService(t *testing.T, cfg string, clk *clock) *Service {
	c := mustDecode(t, cfg)
	return NewService(func(context.Context) Config { return c }, clk.now)
}

func TestSessionOutcomes(t *testing.T) {
	clk := &clock{t: time.Unix(2_000_000, 0)}
	cfgFor := func(mode string, sw, keep bool) string {
		return fmt.Sprintf(`{"session_mode":%q,"switch_on_success":%v,"keep_on_channel_disabled":%v,"rules":[{"name":"r","key_sources":[{"type":"gjson","path":"k"}],
			"param_override_template":{"operations":[{"mode":"pass_headers","value":["Session_id"],"keep_origin":true}]}}]}`, mode, sw, keep)
	}
	user := uuid.New()
	req := Request{UserID: user, Model: "m", Path: "/v1/responses", Body: []byte(`{"k":"s1"}`)}
	a, b := uuid.New(), uuid.New()

	svc := newTestService(t, cfgFor(ModePrefer, true, false), clk)
	ctx := context.Background()
	s := svc.Begin(ctx, req)
	if s == nil || s.Rule != "r" || s.Mode != ModePrefer || s.PassHeaders() == nil {
		t.Fatalf("session = %+v", s)
	}
	if _, ok := s.Bound(); ok {
		t.Fatal("no binding yet")
	}
	s.Served(a, true)
	if s.Outcome() != OutcomeNew {
		t.Fatalf("outcome = %s", s.Outcome())
	}
	// Next request: hit.
	s = svc.Begin(ctx, req)
	if id, ok := s.Bound(); !ok || id != a {
		t.Fatalf("bound = %v %v", id, ok)
	}
	s.Routed()
	s.Served(a, true)
	if s.Outcome() != OutcomeHit {
		t.Fatalf("outcome = %s", s.Outcome())
	}
	// Prefer: the bound channel fails, b serves → rebound.
	s = svc.Begin(ctx, req)
	s.Routed()
	if s.StopAfter(a) {
		t.Fatal("prefer must fail over")
	}
	s.Served(b, true)
	if s.Outcome() != OutcomeRebound {
		t.Fatalf("outcome = %s", s.Outcome())
	}
	if id, _ := svc.Begin(ctx, req).Bound(); id != b {
		t.Fatal("binding did not move")
	}
	// A fallback model never rebinds.
	s = svc.Begin(ctx, req)
	s.Routed()
	s.Served(a, false)
	if s.Outcome() != OutcomeFailover {
		t.Fatalf("outcome = %s", s.Outcome())
	}
	// Broken: the bound channel is not a candidate → dropped, new binding.
	s = svc.Begin(ctx, req)
	s.Broken()
	s.Served(a, true)
	if s.Outcome() != OutcomeBroken {
		t.Fatalf("outcome = %s", s.Outcome())
	}
	if id, _ := svc.Begin(ctx, req).Bound(); id != a {
		t.Fatal("broken binding not replaced")
	}
	// Another user with the same value is independent.
	if _, ok := svc.Begin(ctx, Request{UserID: uuid.New(), Model: "m", Body: []byte(`{"k":"s1"}`)}).Bound(); ok {
		t.Fatal("binding leaked to another user")
	}
	if st := svc.Stats(ctx); st.Entries != 1 || st.Rules["r"] != 1 || st.MaxEntries != 100000 {
		t.Fatalf("stats = %+v", st)
	}
	// Failed request without a binding: miss, nothing bound.
	s = svc.Begin(ctx, Request{UserID: user, Model: "m", Body: []byte(`{"k":"s2"}`)})
	if s.Outcome() != OutcomeMiss {
		t.Fatalf("outcome = %s", s.Outcome())
	}

	// Strict: the bound channel's failure stops the request; binding kept.
	svc = newTestService(t, cfgFor(ModeStrict, true, false), clk)
	svc.Begin(ctx, req).Served(a, true)
	s = svc.Begin(ctx, req)
	if s.StopAfter(a) {
		t.Fatal("not routed to the bound channel: no strict stop")
	}
	s.Routed()
	if s.StopAfter(b) || !s.StopAfter(a) || s.Outcome() != OutcomeStrictFailed {
		t.Fatalf("strict: outcome = %s", s.Outcome())
	}
	if id, _ := svc.Begin(ctx, req).Bound(); id != a {
		t.Fatal("strict failure must keep the binding")
	}

	// switch_on_success = false: served elsewhere keeps the old binding.
	svc = newTestService(t, cfgFor(ModePrefer, false, false), clk)
	svc.Begin(ctx, req).Served(a, true)
	s = svc.Begin(ctx, req)
	s.Routed()
	s.Served(b, true)
	if s.Outcome() != OutcomeFailover {
		t.Fatalf("outcome = %s", s.Outcome())
	}
	if id, _ := svc.Begin(ctx, req).Bound(); id != a {
		t.Fatal("binding must stay on a")
	}

	// keep_on_channel_disabled: a broken binding survives being served elsewhere.
	svc = newTestService(t, cfgFor(ModePrefer, true, true), clk)
	svc.Begin(ctx, req).Served(a, true)
	s = svc.Begin(ctx, req)
	s.Broken()
	s.Served(b, true)
	if id, _ := svc.Begin(ctx, req).Bound(); id != a || s.Outcome() != OutcomeBroken {
		t.Fatalf("kept binding = %v, outcome %s", id, s.Outcome())
	}

	// Mode off: headers only, never bound.
	svc = newTestService(t, cfgFor(ModeOff, true, false), clk)
	s = svc.Begin(ctx, req)
	s.Served(a, true)
	if s.Outcome() != OutcomeOff || s.PassHeaders() == nil {
		t.Fatalf("off: %s", s.Outcome())
	}
	if _, ok := svc.Begin(ctx, req).Bound(); ok || svc.Stats(ctx).Entries != 0 {
		t.Fatal("off must not bind")
	}

	// TTL: rule ttl_seconds overrides the default and slides.
	svc = newTestService(t, `{"default_ttl_seconds":3600,"rules":[{"name":"r","ttl_seconds":10,"key_sources":[{"type":"gjson","path":"k"}]}]}`, clk)
	svc.Begin(ctx, req).Served(a, true)
	clk.add(9 * time.Second)
	if _, ok := svc.Begin(ctx, req).Bound(); !ok {
		t.Fatal("expired early")
	}
	clk.add(9 * time.Second)
	if _, ok := svc.Begin(ctx, req).Bound(); !ok {
		t.Fatal("hit did not slide the TTL")
	}
	clk.add(11 * time.Second)
	if _, ok := svc.Begin(ctx, req).Bound(); ok {
		t.Fatal("binding must expire")
	}

	// A nil session (no rule applied) is inert.
	var none *Session
	none.Routed()
	none.Broken()
	none.Served(a, true)
	if none.Outcome() != "" || none.StopAfter(a) || none.PassHeaders() != nil {
		t.Fatal("nil session must be inert")
	}
}
