// Package plugin implements OmniGate's channel plugins: manifest validation,
// TypeScript compilation, risk scanning, versioning/approval, capabilities and
// request hooks. The JavaScript sandbox lives in plugin/engine.
package plugin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const SDKVersion = 1

var (
	pluginIDRe   = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9-]+)+$`)
	semverRe     = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)
	hostRe       = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)
	capNameRe    = regexp.MustCompile(`^(models\.list|balance\.get|usage\.query|quota\.get|health\.check|custom\.[a-zA-Z][a-zA-Z0-9_]{0,40})$`)
	fieldNameRe  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,40}$`)
	outputKinds  = []string{"balance", "quota", "models", "usage", "health", "json"}
	knownHooks   = []string{"transformRequest", "signRequest"}
	knownExtends = map[string]string{"openai.chat": "openai", "anthropic.messages": "anthropic"}
	meterNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	// protocolHooks implement a custom upstream protocol (phase9-api.md §2);
	// the first three are required when protocol is "custom".
	protocolHooks = []string{"buildRequest", "parseResponse", "parseStream", "endStream", "normalizeError"}
)

// Plugin kinds and protocols (phase9-api.md §2, §3).
const (
	KindChannel    = "channel"
	KindBilling    = "billing"
	ProtocolCustom = "custom"
	// ChannelTypeCustom is the channel type of custom-protocol plugins.
	ChannelTypeCustom = "custom"
)

// MeterDecl declares a billing meter; computeUnits lives in the code
// (billing.meters.<name>.computeUnits).
type MeterDecl struct {
	Label string `json:"label"`
	Unit  string `json:"unit,omitempty"`
}

// BillingDecl is the manifest's billing section (kind includes "billing").
type BillingDecl struct {
	Meters map[string]MeterDecl `json:"meters"`
}

type ModelDefault struct {
	Model         string `json:"model"`
	UpstreamModel string `json:"upstreamModel"`
}

type Defaults struct {
	BaseURL string         `json:"baseUrl,omitempty"`
	Models  []ModelDefault `json:"models,omitempty"`
}

type StorageQuota struct {
	MaxKeys  int `json:"maxKeys"`
	MaxBytes int `json:"maxBytes"`
}

type Permissions struct {
	Network   []string      `json:"network"`
	Secrets   []string      `json:"secrets"`
	Storage   *StorageQuota `json:"storage,omitempty"`
	Schedule  []string      `json:"schedule"`
	Dangerous []string      `json:"dangerous"`
}

type Schedule struct {
	MinInterval string `json:"minInterval"`
}

type Capability struct {
	Output          string    `json:"output"`
	UserTriggerable bool      `json:"userTriggerable"`
	Schedule        *Schedule `json:"schedule,omitempty"`
	CacheTTL        string    `json:"cacheTtl,omitempty"`
	Timeout         string    `json:"timeout,omitempty"`
	Label           string    `json:"label,omitempty"`
}

// TimeoutDuration returns the effective timeout (default 10s, max 60s).
func (c Capability) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d <= 0 {
		return 10 * time.Second
	}
	return min(d, 60*time.Second)
}

// MinInterval returns the schedule interval, or 0 when not schedulable.
func (c Capability) MinInterval() time.Duration {
	if c.Schedule == nil {
		return 0
	}
	d, _ := time.ParseDuration(c.Schedule.MinInterval)
	return d
}

type Manifest struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	Version         string                `json:"version"`
	SDK             int                   `json:"sdk"`
	Description     string                `json:"description,omitempty"`
	Author          string                `json:"author,omitempty"`
	Homepage        string                `json:"homepage,omitempty"`
	Kind            []string              `json:"kind,omitempty"`
	Extends         string                `json:"extends"`
	Protocol        string                `json:"protocol,omitempty"`
	Entry           string                `json:"entry,omitempty"`
	Defaults        Defaults              `json:"defaults"`
	ConfigSchema    *ConfigSchema         `json:"configSchema,omitempty"`
	Permissions     Permissions           `json:"permissions"`
	Capabilities    map[string]Capability `json:"capabilities"`
	Hooks           []string              `json:"hooks"`
	UIContributions []json.RawMessage     `json:"uiContributions"`
	Billing         *BillingDecl          `json:"billing,omitempty"`
}

// ChannelType is the channel type implied by Extends / Protocol ("" for
// plugins that are not channel plugins).
func (m *Manifest) ChannelType() string {
	switch {
	case !m.HasKind(KindChannel):
		return ""
	case m.Protocol == ProtocolCustom:
		return ChannelTypeCustom
	}
	return knownExtends[m.Extends]
}

// HasKind reports whether the plugin declares kind k (no kind = channel).
func (m *Manifest) HasKind(k string) bool {
	if len(m.Kind) == 0 {
		return k == KindChannel
	}
	return slices.Contains(m.Kind, k)
}

// CustomProtocol reports whether the plugin implements the upstream protocol.
func (m *Manifest) CustomProtocol() bool {
	return m.Protocol == ProtocolCustom && m.HasKind(KindChannel)
}

// Meter returns the declaration of a billing meter.
func (m *Manifest) Meter(name string) (MeterDecl, bool) {
	if m.Billing == nil || !m.HasKind(KindBilling) {
		return MeterDecl{}, false
	}
	d, ok := m.Billing.Meters[name]
	return d, ok
}

func (m *Manifest) HasHook(h string) bool { return slices.Contains(m.Hooks, h) }

// SecretFields lists configSchema properties marked x-secret.
func (m *Manifest) SecretFields() []string {
	if m.ConfigSchema == nil {
		return nil
	}
	var out []string
	for name, p := range m.ConfigSchema.Properties {
		if p.Secret {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Diagnostic is a build/validation message for the editor.
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Severity string `json:"severity"` // error | warning
	Message  string `json:"message"`
}

func merr(format string, a ...any) Diagnostic {
	return Diagnostic{File: "manifest.json", Severity: "error", Message: fmt.Sprintf(format, a...)}
}

// ParseManifest parses and validates manifest.json. builtin allows the
// reserved "builtin." prefix and a missing entry.
func ParseManifest(raw []byte, files map[string]string, builtin bool) (*Manifest, []Diagnostic) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, []Diagnostic{merr("manifest.json 不是合法的 JSON 或包含未知字段：%v", err)}
	}
	var ds []Diagnostic
	add := func(format string, a ...any) { ds = append(ds, merr(format, a...)) }

	if !pluginIDRe.MatchString(m.ID) || len(m.ID) > 64 {
		add("id 必须形如 vendor.name（小写字母、数字、点与连字符）")
	} else if strings.HasPrefix(m.ID, "builtin.") && !builtin {
		add("id 前缀 builtin. 为内置插件保留")
	}
	if n := len([]rune(strings.TrimSpace(m.Name))); n == 0 || n > 64 {
		add("name 长度应为 1–64 个字符")
	}
	if !semverRe.MatchString(m.Version) {
		add("version 必须是语义化版本号，例如 1.0.0")
	}
	if m.SDK != SDKVersion {
		add("sdk 必须为 %d", SDKVersion)
	}
	if len(m.Description) > 500 {
		add("description 不能超过 500 个字符")
	}
	if m.Homepage != "" {
		if u, err := url.Parse(m.Homepage); err != nil || u.Scheme != "https" || u.Host == "" {
			add("homepage 必须是 https 地址")
		}
	}
	ds = append(ds, m.validateKinds()...)
	if !builtin {
		if m.Entry == "" {
			m.Entry = "src/index.ts"
		}
		if _, ok := files[m.Entry]; !ok {
			add("入口文件 %s 不存在", m.Entry)
		}
	}
	if m.Defaults.BaseURL != "" {
		if u, err := url.Parse(m.Defaults.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			add("defaults.baseUrl 必须是 http(s) 绝对地址")
		}
	}
	for _, d := range m.Defaults.Models {
		if d.Model == "" || len(d.Model) > 128 {
			add("defaults.models 中的模型名无效")
			break
		}
	}

	// permissions
	p := &m.Permissions
	for _, h := range p.Network {
		if h != "$baseUrl" && !hostRe.MatchString(h) {
			add("permissions.network 中的 %q 不是合法主机名（不允许 IP、端口或路径；可用 *.example.com 或 $baseUrl）", h)
		}
	}
	for _, s := range p.Secrets {
		if !fieldNameRe.MatchString(s) {
			add("permissions.secrets 中的 %q 名称无效", s)
		}
	}
	if p.Storage != nil && (p.Storage.MaxKeys < 1 || p.Storage.MaxKeys > 1000 || p.Storage.MaxBytes < 1 || p.Storage.MaxBytes > 1<<20) {
		add("permissions.storage 配额超出范围（maxKeys 1–1000，maxBytes 1–1048576）")
	}
	if len(p.Dangerous) > 0 {
		add("当前版本不接受任何危险权限（permissions.dangerous 必须为空）")
	}

	// capabilities
	for name, c := range m.Capabilities {
		if !capNameRe.MatchString(name) {
			add("能力名 %q 无效（内置能力或 custom.<name>）", name)
			continue
		}
		if !slices.Contains(outputKinds, c.Output) {
			add("能力 %s 的 output 必须是 %s 之一", name, strings.Join(outputKinds, " / "))
		}
		if c.Schedule != nil {
			d, err := time.ParseDuration(c.Schedule.MinInterval)
			if err != nil || d < time.Minute {
				add("能力 %s 的 schedule.minInterval 必须是不少于 1m 的时长", name)
			}
			if !slices.Contains(p.Schedule, name) {
				add("能力 %s 声明了定时执行，但 permissions.schedule 未包含它", name)
			}
		}
		for field, v := range map[string]string{"timeout": c.Timeout, "cacheTtl": c.CacheTTL} {
			if v == "" {
				continue
			}
			if d, err := time.ParseDuration(v); err != nil || d <= 0 || (field == "timeout" && d > 60*time.Second) {
				add("能力 %s 的 %s 无效（timeout 最大 60s）", name, field)
			}
		}
	}
	for _, s := range p.Schedule {
		if _, ok := m.Capabilities[s]; !ok {
			add("permissions.schedule 中的 %q 不是已声明的能力", s)
		}
	}
	ds = append(ds, m.validateHooks()...)
	if m.ConfigSchema != nil {
		ds = append(ds, m.ConfigSchema.validate(p.Secrets)...)
	}
	ds = append(ds, validateUI(m.UIContributions, m.Capabilities)...)
	if m.Capabilities == nil {
		m.Capabilities = map[string]Capability{}
	}
	return &m, ds
}

// validateKinds checks kind, extends / protocol and the billing section.
func (m *Manifest) validateKinds() []Diagnostic {
	var ds []Diagnostic
	add := func(format string, a ...any) { ds = append(ds, merr(format, a...)) }
	seen := map[string]bool{}
	for _, k := range m.Kind {
		if k != KindChannel && k != KindBilling {
			add("kind 中的 %q 无效（只能是 channel 或 billing）", k)
		}
		if seen[k] {
			add("kind 中的 %q 重复", k)
		}
		seen[k] = true
	}
	if len(m.Kind) == 0 {
		m.Kind = []string{KindChannel}
	}
	if m.HasKind(KindChannel) {
		switch {
		case m.Protocol != "" && m.Protocol != ProtocolCustom:
			add("protocol 只能是 \"custom\"（或省略并使用 extends）")
		case m.Protocol == ProtocolCustom && m.Extends != "":
			add("protocol: \"custom\" 与 extends 互斥：自定义协议插件自己实现上游协议，不继承内置协议")
		case m.Protocol == "" && knownExtends[m.Extends] == "":
			add("extends 必须是 openai.chat 或 anthropic.messages，或声明 protocol: \"custom\" 实现自定义协议")
		}
	} else {
		if m.Extends != "" || m.Protocol != "" {
			add("不含 channel 的插件（仅计费）不能声明 extends 或 protocol")
		}
		if len(m.Hooks) > 0 || len(m.Capabilities) > 0 || len(m.UIContributions) > 0 {
			add("不含 channel 的插件（仅计费）不能声明 hooks、capabilities 或 uiContributions")
		}
	}
	if m.HasKind(KindBilling) {
		if m.Billing == nil || len(m.Billing.Meters) == 0 {
			add("kind 包含 billing 时必须在 billing.meters 中声明至少一个计量")
		}
	} else if m.Billing != nil {
		add("声明了 billing，但 kind 不包含 billing")
	}
	if m.Billing != nil {
		if len(m.Billing.Meters) > 20 {
			add("billing.meters 最多 20 个计量")
		}
		for name, d := range m.Billing.Meters {
			if !meterNameRe.MatchString(name) {
				add("计量名 %q 无效（小写字母开头，只含小写字母、数字和 _，最长 41 个字符）", name)
			}
			if n := len([]rune(strings.TrimSpace(d.Label))); n == 0 || n > 64 {
				add("计量 %s 的 label 长度应为 1–64 个字符", name)
			}
			if len([]rune(d.Unit)) > 16 {
				add("计量 %s 的 unit 不能超过 16 个字符", name)
			}
		}
	}
	return ds
}

// validateHooks checks hooks against the plugin's protocol.
func (m *Manifest) validateHooks() []Diagnostic {
	var ds []Diagnostic
	add := func(format string, a ...any) { ds = append(ds, merr(format, a...)) }
	custom := m.Protocol == ProtocolCustom
	for _, h := range m.Hooks {
		switch {
		case slices.Contains(protocolHooks, h):
			if !custom {
				add("Hook %s 只用于 protocol: \"custom\" 的插件", h)
			}
		case h == "transformRequest" && custom:
			add("protocol: \"custom\" 的插件由 buildRequest 构造上游请求，不支持 transformRequest")
		case !slices.Contains(knownHooks, h):
			add("hooks 中的 %q 不受支持（transformRequest、signRequest；自定义协议另有 %s）", h, strings.Join(protocolHooks, "、"))
		}
	}
	if custom && m.HasKind(KindChannel) {
		for _, h := range protocolHooks[:3] {
			if !slices.Contains(m.Hooks, h) {
				add("protocol: \"custom\" 的插件必须实现并在 hooks 中声明 %s", h)
			}
		}
	}
	return ds
}

// ---- config schema subset ----

type SchemaProperty struct {
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Enum        []any    `json:"enum,omitempty"`
	Minimum     *float64 `json:"minimum,omitempty"`
	Maximum     *float64 `json:"maximum,omitempty"`
	MinLength   *int     `json:"minLength,omitempty"`
	MaxLength   *int     `json:"maxLength,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Secret      bool     `json:"x-secret,omitempty"`
	Group       string   `json:"x-group,omitempty"`
	Help        string   `json:"x-help,omitempty"`
}

type ConfigSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]SchemaProperty `json:"properties"`
	Required   []string                  `json:"required,omitempty"`
}

func (s *ConfigSchema) validate(secretPerms []string) []Diagnostic {
	var ds []Diagnostic
	add := func(format string, a ...any) { ds = append(ds, merr(format, a...)) }
	if s.Type != "object" {
		add("configSchema.type 必须为 object")
	}
	if len(s.Properties) > 32 {
		add("configSchema 最多 32 个属性")
	}
	for name, p := range s.Properties {
		if !fieldNameRe.MatchString(name) || name == "apiKey" {
			add("configSchema 属性名 %q 无效（apiKey 为渠道主密钥保留）", name)
		}
		switch p.Type {
		case "string", "number", "integer", "boolean":
		default:
			add("configSchema.%s.type 只能是 string / number / integer / boolean", name)
		}
		if p.Pattern != "" {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				add("configSchema.%s.pattern 不是合法的正则", name)
			}
		}
		if p.Secret && !slices.Contains(secretPerms, name) {
			add("configSchema.%s 标记为 x-secret，但 permissions.secrets 未包含它", name)
		}
		if p.Secret && p.Type != "string" {
			add("configSchema.%s：x-secret 字段必须是 string", name)
		}
	}
	for _, r := range s.Required {
		if _, ok := s.Properties[r]; !ok {
			add("configSchema.required 中的 %q 不是已声明的属性", r)
		}
	}
	return ds
}

// ValidateConfig checks non-secret config values (and applies defaults).
// Secret fields are validated separately because they are write-only.
func (s *ConfigSchema) ValidateConfig(cfg map[string]any, secretsSet map[string]bool) (map[string]any, map[string]any) {
	out := map[string]any{}
	details := map[string]any{}
	if s == nil {
		for k := range cfg {
			details["pluginConfig."+k] = "该插件没有声明配置项"
		}
		return out, details
	}
	for k := range cfg {
		if p, ok := s.Properties[k]; !ok || p.Secret {
			details["pluginConfig."+k] = "未声明的配置项（敏感字段请通过 secrets 提交）"
		}
	}
	for name, p := range s.Properties {
		if p.Secret {
			if slices.Contains(s.Required, name) && !secretsSet[name] {
				details["secrets."+name] = "必填"
			}
			continue
		}
		v, ok := cfg[name]
		if !ok || v == nil {
			if p.Default != nil {
				out[name] = p.Default
			} else if slices.Contains(s.Required, name) {
				details["pluginConfig."+name] = "必填"
			}
			continue
		}
		if msg := p.check(v); msg != "" {
			details["pluginConfig."+name] = msg
			continue
		}
		out[name] = v
	}
	return out, details
}

func (p SchemaProperty) check(v any) string {
	switch p.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return "必须是字符串"
		}
		if p.MinLength != nil && len([]rune(s)) < *p.MinLength || p.MaxLength != nil && len([]rune(s)) > *p.MaxLength {
			return "长度不符合要求"
		}
		if p.Pattern != "" {
			if re, err := regexp.Compile(p.Pattern); err == nil && !re.MatchString(s) {
				return "格式不符合要求"
			}
		}
	case "number", "integer":
		f, ok := v.(float64)
		if !ok {
			return "必须是数字"
		}
		if p.Type == "integer" && f != float64(int64(f)) {
			return "必须是整数"
		}
		if p.Minimum != nil && f < *p.Minimum || p.Maximum != nil && f > *p.Maximum {
			return "超出允许范围"
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return "必须是布尔值"
		}
	}
	if len(p.Enum) > 0 && !slices.ContainsFunc(p.Enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) }) {
		return "不在允许的取值范围内"
	}
	return ""
}

// ---- permission diff ----

// PermissionDiff describes how permissions changed relative to a baseline.
type PermissionDiff struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

func (d PermissionDiff) Empty() bool { return len(d.Added) == 0 && len(d.Removed) == 0 }

func (p Permissions) flatten() []string {
	var out []string
	for _, h := range p.Network {
		out = append(out, "network:"+h)
	}
	for _, s := range p.Secrets {
		out = append(out, "secret:"+s)
	}
	for _, s := range p.Schedule {
		out = append(out, "schedule:"+s)
	}
	if p.Storage != nil {
		out = append(out, "storage:"+strconv.Itoa(p.Storage.MaxKeys)+"keys/"+strconv.Itoa(p.Storage.MaxBytes)+"B")
	}
	for _, s := range p.Dangerous {
		out = append(out, "dangerous:"+s)
	}
	sort.Strings(out)
	return out
}

// DiffPermissions compares next against prev (nil prev = everything added).
func DiffPermissions(prev *Permissions, next Permissions) PermissionDiff {
	n := next.flatten()
	if n == nil {
		n = []string{}
	}
	if prev == nil {
		return PermissionDiff{Added: n, Removed: []string{}}
	}
	o := prev.flatten()
	d := PermissionDiff{Added: []string{}, Removed: []string{}}
	for _, x := range n {
		if !slices.Contains(o, x) {
			d.Added = append(d.Added, x)
		}
	}
	for _, x := range o {
		if !slices.Contains(n, x) {
			d.Removed = append(d.Removed, x)
		}
	}
	return d
}

// CompareSemver returns -1, 0 or 1 (pre-release ignored except as tie-breaker:
// a pre-release sorts before its release).
func CompareSemver(a, b string) int {
	pa, pb := semverParts(a), semverParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	ra, rb := strings.Contains(a, "-"), strings.Contains(b, "-")
	switch {
	case ra && !rb:
		return -1
	case !ra && rb:
		return 1
	}
	return strings.Compare(a, b)
}

func semverParts(v string) [3]int {
	var out [3]int
	core, _, _ := strings.Cut(v, "-")
	for i, s := range strings.SplitN(core, ".", 3) {
		out[i], _ = strconv.Atoi(s)
	}
	return out
}
