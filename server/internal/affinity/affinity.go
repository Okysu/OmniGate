// Package affinity implements session (channel) affinity
// (docs/contracts/phase12-api.md): requests of one client session — recognised
// by the client's own identifiers such as Codex CLI's prompt_cache_key or
// Claude Code's metadata.user_id — are routed to the channel that served the
// session before, so upstream prompt caches and account pools keep hitting.
// Rules use the JSON shape of new-api's "channel affinity" setting, so its
// templates can be pasted as-is.
//
// Bindings live in an in-memory LRU with per-entry sliding TTL (single
// instance deployment); only a SHA-256 of the binding key is kept, never the
// client's raw session value.
package affinity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"omnigate/internal/channel"
)

// Session modes.
const (
	ModeOff    = "off"    // the rule still passes headers, but sessions are not pinned
	ModePrefer = "prefer" // pinned; a failing bound channel fails over as usual
	ModeStrict = "strict" // pinned; a failing bound channel returns the error (no failover)
	// ModeInherit (rules only) uses the global session_mode.
	ModeInherit = "inherit"
)

// Key source types.
const (
	SourceGJSON  = "gjson"          // path into the JSON request body
	SourceHeader = "request_header" // request header
)

// OpPassHeaders is the only supported param_override_template operation.
const OpPassHeaders = "pass_headers"

// Limits.
const (
	MaxRules        = 50
	MaxKeySources   = 16
	MaxPassHeaders  = 64
	MaxOperations   = 8
	MaxPatterns     = 32
	maxPatternLen   = 512
	maxNameLen      = 64
	maxPathLen      = 256
	maxTTLSeconds   = 30 * 24 * 3600
	MaxEntriesLimit = 1_000_000
	maxReported     = 5
)

// KeySource locates the session value in a request.
type KeySource struct {
	Type string `json:"type"`
	Key  string `json:"key,omitempty"`  // request_header
	Path string `json:"path,omitempty"` // gjson
}

// Operation is one param_override_template operation (pass_headers only).
type Operation struct {
	Mode       string   `json:"mode"`
	Value      []string `json:"value"`
	KeepOrigin bool     `json:"keep_origin"`
}

// Template is a rule's param_override_template.
type Template struct {
	Operations []Operation `json:"operations"`
}

// Rule is one affinity rule (field names follow new-api).
type Rule struct {
	Name                  string      `json:"name"`
	ModelRegex            []string    `json:"model_regex"`
	PathRegex             []string    `json:"path_regex"`
	UserAgentInclude      []string    `json:"user_agent_include"`
	KeySources            []KeySource `json:"key_sources"`
	ValueRegex            string      `json:"value_regex"`
	TTLSeconds            int         `json:"ttl_seconds"`
	ParamOverrideTemplate *Template   `json:"param_override_template"`
	SkipRetryOnFailure    bool        `json:"skip_retry_on_failure"`
	SessionMode           string      `json:"session_mode"`
	IncludeUsingGroup     bool        `json:"include_using_group"`
	IncludeModelName      bool        `json:"include_model_name"`
	IncludeRuleName       bool        `json:"include_rule_name"`

	model, path []*regexp.Regexp
	value       *regexp.Regexp
}

// Config is the gateway.affinity system setting.
type Config struct {
	Enabled               bool   `json:"enabled"`
	SessionMode           string `json:"session_mode"`
	SwitchOnSuccess       bool   `json:"switch_on_success"`
	KeepOnChannelDisabled bool   `json:"keep_on_channel_disabled"`
	MaxEntries            int    `json:"max_entries"`
	DefaultTTLSeconds     int    `json:"default_ttl_seconds"`
	Rules                 []Rule `json:"rules"`
}

// Mode returns the effective session mode of r under the global mode: an
// explicit off / prefer / strict wins, inherit uses the global mode, and an
// empty session_mode is new-api's legacy form (skip_retry_on_failure = strict,
// otherwise the global mode).
func (r *Rule) Mode(global string) string {
	switch r.SessionMode {
	case ModeOff, ModePrefer, ModeStrict:
		return r.SessionMode
	case "":
		if r.SkipRetryOnFailure {
			return ModeStrict
		}
	}
	return global
}

// Pass returns the rule's pass_headers (merged over all operations; nil =
// none). KeepOrigin is set when any operation keeps the channel's values.
func (r *Rule) Pass() *channel.PassHeaders {
	if r.ParamOverrideTemplate == nil {
		return nil
	}
	var out channel.PassHeaders
	for _, op := range r.ParamOverrideTemplate.Operations {
		for _, h := range op.Value {
			if !slices.ContainsFunc(out.Names, func(n string) bool { return strings.EqualFold(n, h) }) {
				out.Names = append(out.Names, h)
			}
		}
		out.KeepOrigin = out.KeepOrigin || op.KeepOrigin
	}
	if len(out.Names) == 0 {
		return nil
	}
	return &out
}

func passTemplate(headers ...string) *Template {
	return &Template{Operations: []Operation{{Mode: OpPassHeaders, Value: headers, KeepOrigin: true}}}
}

// Codex CLI and Claude Code headers passed through by the presets (the lists
// of new-api's templates).
var (
	codexHeaders = []string{"Originator", "Session_id", "Thread_id", "Session-Id", "Thread-Id", "X-Client-Request-Id", "User-Agent",
		"X-Codex-Beta-Features", "X-Codex-Turn-State", "X-Codex-Turn-Metadata", "X-Codex-Window-Id", "X-Codex-Parent-Thread-Id",
		"X-OpenAI-Subagent", "X-OpenAI-Memgen-Request", "X-ResponsesAPI-Include-Timing-Metrics", "X-OpenAI-Internal-Codex-Responses-Lite"}
	claudeHeaders = []string{"X-Stainless-Arch", "X-Stainless-Lang", "X-Stainless-Os", "X-Stainless-Package-Version", "X-Stainless-Retry-Count",
		"X-Stainless-Runtime", "X-Stainless-Runtime-Version", "X-Stainless-Timeout", "User-Agent", "X-App", "Anthropic-Beta",
		"Anthropic-Dangerous-Direct-Browser-Access", "Anthropic-Version", "X-Claude-Code-Session-Id"}
)

// Default is the default setting (compiled): enabled, with the Codex CLI and
// Claude Code presets. They match any model (the key sources only exist for
// those clients, so this also covers Codex / Claude Code used with other
// models) and prefer the bound channel without giving up failover.
func Default() Config {
	raw, _ := json.Marshal(defaults())
	c, msg := Decode(raw)
	if msg != "" {
		panic("affinity: invalid default setting: " + msg)
	}
	return c
}

func defaults() Config {
	return Config{Enabled: true, SessionMode: ModePrefer, SwitchOnSuccess: true, MaxEntries: 100_000, DefaultTTLSeconds: 3600,
		Rules: []Rule{
			{Name: "codex cli trace", ModelRegex: []string{".*"}, PathRegex: []string{"^/v1/responses"}, UserAgentInclude: []string{},
				KeySources:            []KeySource{{Type: SourceGJSON, Path: "prompt_cache_key"}, {Type: SourceHeader, Key: "Session_id"}, {Type: SourceHeader, Key: "Session-Id"}},
				ParamOverrideTemplate: passTemplate(codexHeaders...), SessionMode: ModePrefer, IncludeUsingGroup: true, IncludeRuleName: true},
			{Name: "claude cli trace", ModelRegex: []string{".*"}, PathRegex: []string{"^/v1/messages"}, UserAgentInclude: []string{},
				KeySources:            []KeySource{{Type: SourceGJSON, Path: "metadata.user_id"}, {Type: SourceHeader, Key: "X-Claude-Code-Session-Id"}},
				ParamOverrideTemplate: passTemplate(claudeHeaders...), SessionMode: ModePrefer, IncludeUsingGroup: true, IncludeRuleName: true},
		}}
}

// Decode parses and validates a setting document (the JSON shape of new-api's
// channel affinity setting; unknown fields are ignored so new-api documents
// paste as-is). Missing global fields take their defaults. It returns the
// canonical, compiled config or a Chinese message naming the invalid fields.
func Decode(raw json.RawMessage) (Config, string) {
	var in struct {
		Enabled               *bool             `json:"enabled"`
		SessionMode           *string           `json:"session_mode"`
		SwitchOnSuccess       *bool             `json:"switch_on_success"`
		KeepOnChannelDisabled *bool             `json:"keep_on_channel_disabled"`
		MaxEntries            *int              `json:"max_entries"`
		DefaultTTLSeconds     *int              `json:"default_ttl_seconds"`
		Rules                 []json.RawMessage `json:"rules"`
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &in) != nil {
		return Config{}, "必须是 JSON 对象（会话亲和设置）"
	}
	d := defaults()
	c := Config{Enabled: d.Enabled, SessionMode: d.SessionMode, SwitchOnSuccess: d.SwitchOnSuccess, MaxEntries: d.MaxEntries,
		DefaultTTLSeconds: d.DefaultTTLSeconds, Rules: []Rule{}}
	var errs []string
	fail := func(field, format string, a ...any) { errs = append(errs, field+"："+fmt.Sprintf(format, a...)) }
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.SwitchOnSuccess != nil {
		c.SwitchOnSuccess = *in.SwitchOnSuccess
	}
	if in.KeepOnChannelDisabled != nil {
		c.KeepOnChannelDisabled = *in.KeepOnChannelDisabled
	}
	if in.SessionMode != nil && *in.SessionMode != "" {
		c.SessionMode = *in.SessionMode
		if c.SessionMode != ModeOff && c.SessionMode != ModePrefer && c.SessionMode != ModeStrict {
			fail("session_mode", "只能是 off、prefer、strict")
		}
	}
	if in.MaxEntries != nil {
		c.MaxEntries = *in.MaxEntries
		if c.MaxEntries < 1 || c.MaxEntries > MaxEntriesLimit {
			fail("max_entries", "范围为 1–%d", MaxEntriesLimit)
		}
	}
	if in.DefaultTTLSeconds != nil {
		c.DefaultTTLSeconds = *in.DefaultTTLSeconds
		if c.DefaultTTLSeconds < 1 || c.DefaultTTLSeconds > maxTTLSeconds {
			fail("default_ttl_seconds", "范围为 1–%d 秒（30 天）", maxTTLSeconds)
		}
	}
	if len(in.Rules) > MaxRules {
		fail("rules", "最多 %d 条规则", MaxRules)
		in.Rules = in.Rules[:MaxRules]
	}
	names := map[string]bool{}
	for i, rr := range in.Rules {
		r, rerrs := decodeRule(rr, fmt.Sprintf("rules[%d]", i))
		errs = append(errs, rerrs...)
		if r.Name != "" {
			if names[r.Name] {
				fail(fmt.Sprintf("rules[%d].name", i), "规则名称 %q 重复", r.Name)
			}
			names[r.Name] = true
		}
		c.Rules = append(c.Rules, r)
	}
	if len(errs) > 0 {
		if len(errs) > maxReported {
			errs = append(errs[:maxReported], fmt.Sprintf("…另有 %d 处错误", len(errs)-maxReported))
		}
		return Config{}, strings.Join(errs, "；")
	}
	return c, ""
}

// decodeRule validates and compiles one rule; p is its field path.
func decodeRule(raw json.RawMessage, p string) (Rule, []string) {
	var errs []string
	fail := func(field, format string, a ...any) { errs = append(errs, p+field+"："+fmt.Sprintf(format, a...)) }
	var in struct {
		Rule
		ParamOverrideTemplate json.RawMessage `json:"param_override_template"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Rule{}, []string{p + "：格式错误（" + jsonErr(err) + "）"}
	}
	r := in.Rule
	r.ParamOverrideTemplate = nil
	r.Name = strings.TrimSpace(r.Name)
	if n := len([]rune(r.Name)); n == 0 || n > maxNameLen {
		fail(".name", "必填，最多 %d 个字符", maxNameLen)
	}
	var msg string
	if r.ModelRegex, r.model, msg = compileAll(r.ModelRegex); msg != "" {
		fail(".model_regex", "%s", msg)
	}
	if r.PathRegex, r.path, msg = compileAll(r.PathRegex); msg != "" {
		fail(".path_regex", "%s", msg)
	}
	ua := []string{}
	for _, s := range r.UserAgentInclude {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(ua, s) {
			ua = append(ua, s)
		}
	}
	if len(ua) > MaxPatterns {
		fail(".user_agent_include", "最多 %d 项", MaxPatterns)
	}
	r.UserAgentInclude = ua
	switch {
	case len(r.KeySources) == 0:
		fail(".key_sources", "至少需要一个 Key 来源")
	case len(r.KeySources) > MaxKeySources:
		fail(".key_sources", "最多 %d 个 Key 来源", MaxKeySources)
	}
	for i, ks := range r.KeySources {
		f := fmt.Sprintf(".key_sources[%d]", i)
		ks.Key, ks.Path = strings.TrimSpace(ks.Key), strings.TrimSpace(ks.Path)
		switch ks.Type {
		case SourceGJSON:
			if ks.Path == "" || len(ks.Path) > maxPathLen {
				fail(f+".path", "gjson 来源需要 path（请求体 JSON 路径，最多 %d 个字符）", maxPathLen)
			}
			ks.Key = ""
		case SourceHeader:
			if !channel.ValidHeaderName(ks.Key) {
				fail(f+".key", "request_header 来源需要合法的请求头名称（字母、数字、- 和 _，最多 64 个字符）")
			}
			ks.Path = ""
		case "context_int", "context_string":
			fail(f+".type", "%s 是 new-api 内部上下文类型，OmniGate 不支持；请改用 gjson 或 request_header", ks.Type)
		default:
			fail(f+".type", "只能是 gjson 或 request_header")
		}
		r.KeySources[i] = ks
	}
	if r.ValueRegex = strings.TrimSpace(r.ValueRegex); r.ValueRegex != "" {
		re, err := compile(r.ValueRegex)
		if err != "" {
			fail(".value_regex", "%s", err)
		}
		r.value = re
	}
	if r.TTLSeconds < 0 || r.TTLSeconds > maxTTLSeconds {
		fail(".ttl_seconds", "范围为 0–%d 秒（0 = 使用全局默认）", maxTTLSeconds)
	}
	switch r.SessionMode {
	case "", ModeInherit, ModeOff, ModePrefer, ModeStrict:
	default:
		fail(".session_mode", "只能是空、inherit、off、prefer、strict")
	}
	tpl, terrs := decodeTemplate(in.ParamOverrideTemplate, p+".param_override_template")
	errs = append(errs, terrs...)
	r.ParamOverrideTemplate = tpl
	return r, errs
}

// decodeTemplate accepts null / {} or {"operations": [pass_headers…]}.
func decodeTemplate(raw json.RawMessage, p string) (*Template, []string) {
	if isEmpty(raw) {
		return nil, nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil, []string{p + "：必须是 JSON 对象"}
	}
	var errs []string
	for k, v := range top {
		if k != "operations" && !isEmpty(v) {
			errs = append(errs, fmt.Sprintf("%s.%s：不支持（OmniGate 只支持 operations 中的 pass_headers 操作）", p, k))
		}
	}
	var ops []map[string]json.RawMessage
	if v, ok := top["operations"]; ok && !isEmpty(v) {
		if json.Unmarshal(v, &ops) != nil {
			return nil, append(errs, p+".operations：必须是数组")
		}
	}
	if len(ops) > MaxOperations {
		errs = append(errs, fmt.Sprintf("%s.operations：最多 %d 个操作", p, MaxOperations))
		ops = ops[:MaxOperations]
	}
	t := &Template{Operations: []Operation{}}
	for i, m := range ops {
		f := fmt.Sprintf("%s.operations[%d]", p, i)
		var op Operation
		var mode string
		_ = json.Unmarshal(m["mode"], &mode)
		if mode != OpPassHeaders {
			errs = append(errs, fmt.Sprintf("%s.mode：不支持 %q（OmniGate 只支持 pass_headers）", f, mode))
			continue
		}
		op.Mode = mode
		for k, v := range m {
			if k != "mode" && k != "value" && k != "keep_origin" && !isEmpty(v) {
				errs = append(errs, fmt.Sprintf("%s.%s：pass_headers 不支持该字段", f, k))
			}
		}
		if v, ok := m["keep_origin"]; ok && !isEmpty(v) && json.Unmarshal(v, &op.KeepOrigin) != nil {
			errs = append(errs, f+".keep_origin：必须是布尔值")
		}
		names, msg := headerList(m["value"])
		if msg != "" {
			errs = append(errs, f+".value："+msg)
		}
		op.Value = names
		t.Operations = append(t.Operations, op)
	}
	if len(t.Operations) == 0 && len(errs) == 0 {
		return nil, nil
	}
	return t, errs
}

// headerList parses pass_headers' value: an array of header names or one
// comma-separated string.
func headerList(raw json.RawMessage) ([]string, string) {
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, "必须是请求头名称数组"
		}
		list = strings.Split(s, ",")
	}
	out := []string{}
	for _, h := range list {
		h = strings.TrimSpace(h)
		if h == "" || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, h) }) {
			continue
		}
		if !channel.ValidHeaderName(h) {
			return nil, fmt.Sprintf("请求头 %q 格式无效（字母、数字、- 和 _，最多 64 个字符）", h)
		}
		if channel.ForbiddenHeader(h) {
			return nil, fmt.Sprintf("请求头 %q 不允许透传（凭据、Cookie、Host、Content-Length 等由网关管理）", h)
		}
		out = append(out, h)
	}
	switch {
	case len(out) == 0:
		return nil, "至少需要一个请求头"
	case len(out) > MaxPassHeaders:
		return nil, fmt.Sprintf("最多 %d 个请求头", MaxPassHeaders)
	}
	return out, ""
}

func compile(p string) (*regexp.Regexp, string) {
	if len(p) > maxPatternLen {
		return nil, fmt.Sprintf("正则最多 %d 个字符", maxPatternLen)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Sprintf("正则 %q 无效：%v", p, err)
	}
	return re, ""
}

// compileAll trims, dedupes and compiles patterns.
func compileAll(in []string) ([]string, []*regexp.Regexp, string) {
	out, res := []string{}, []*regexp.Regexp{}
	for _, p := range in {
		if p = strings.TrimSpace(p); p == "" || slices.Contains(out, p) {
			continue
		}
		re, msg := compile(p)
		if msg != "" {
			return nil, nil, msg
		}
		out, res = append(out, p), append(res, re)
	}
	if len(out) > MaxPatterns {
		return nil, nil, fmt.Sprintf("最多 %d 个正则", MaxPatterns)
	}
	return out, res, ""
}

func isEmpty(v json.RawMessage) bool {
	t := string(bytes.TrimSpace(v))
	return t == "" || t == "null" || t == "{}" || t == "[]" || t == `""` || t == "false"
}

func jsonErr(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return te.Field + " 类型错误"
	}
	return err.Error()
}
