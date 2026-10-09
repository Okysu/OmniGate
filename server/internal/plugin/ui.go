package plugin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Declarative UI contributions (ADR-0003, phase2-api.md §5). The host renders
// them; plugins cannot ship HTML, CSS or scripts.

var (
	uiSlots      = []string{"channel.detail.capabilities", "channel.detail.overview", "channel.list.badge"}
	uiComponents = []string{"statGroup", "stat", "keyValue", "table", "progress", "badge", "alert", "markdown", "link"}
	uiFormats    = []string{"", "text", "number", "money", "percent", "boolean", "datetime", "relativeTime"}
)

type UINode struct {
	Type         string     `json:"type"`
	Label        string     `json:"label,omitempty"`
	Text         string     `json:"text,omitempty"`
	Level        string     `json:"level,omitempty"`
	Href         string     `json:"href,omitempty"`
	Bind         string     `json:"bind,omitempty"`
	CurrencyBind string     `json:"currencyBind,omitempty"`
	ValueBind    string     `json:"valueBind,omitempty"`
	MaxBind      string     `json:"maxBind,omitempty"`
	RowsBind     string     `json:"rowsBind,omitempty"`
	Format       string     `json:"format,omitempty"`
	Columns      []UIColumn `json:"columns,omitempty"`
	Items        []UINode   `json:"items,omitempty"`
}

type UIColumn struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Format string `json:"format,omitempty"`
}

type UIAction struct {
	Label      string `json:"label"`
	Capability string `json:"capability"`
	Confirm    string `json:"confirm,omitempty"`
}

type UIContribution struct {
	Slot      string     `json:"slot"`
	Title     string     `json:"title,omitempty"`
	Component UINode     `json:"component"`
	Actions   []UIAction `json:"actions,omitempty"`
}

func validateUI(raw []json.RawMessage, caps map[string]Capability) []Diagnostic {
	var ds []Diagnostic
	if len(raw) > 20 {
		return []Diagnostic{merr("uiContributions 最多 20 个")}
	}
	for i, r := range raw {
		var c UIContribution
		dec := json.NewDecoder(strings.NewReader(string(r)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			ds = append(ds, merr("uiContributions[%d] 格式错误：%v", i, err))
			continue
		}
		if !slices.Contains(uiSlots, c.Slot) {
			ds = append(ds, merr("uiContributions[%d].slot %q 不受支持", i, c.Slot))
		}
		if c.Slot == "channel.list.badge" && c.Component.Type != "badge" {
			ds = append(ds, merr("uiContributions[%d]：channel.list.badge 只能使用 badge 组件", i))
		}
		for _, msg := range checkNode(c.Component, caps, 0) {
			ds = append(ds, merr("uiContributions[%d]：%s", i, msg))
		}
		for _, a := range c.Actions {
			if _, ok := caps[a.Capability]; !ok {
				ds = append(ds, merr("uiContributions[%d] 的动作引用了未声明的能力 %q", i, a.Capability))
			}
		}
	}
	return ds
}

func checkBind(b string, caps map[string]Capability) string {
	if b == "" {
		return ""
	}
	name, ptr, ok := strings.Cut(b, ":")
	if !ok || !strings.HasPrefix(ptr, "/") && ptr != "" {
		return fmt.Sprintf("绑定 %q 必须形如 <能力名>:<JSON Pointer>", b)
	}
	if _, ok := caps[name]; !ok {
		return fmt.Sprintf("绑定 %q 引用了未声明的能力", b)
	}
	return ""
}

func checkNode(n UINode, caps map[string]Capability, depth int) []string {
	var out []string
	if depth > 3 {
		return []string{"组件嵌套过深"}
	}
	if !slices.Contains(uiComponents, n.Type) {
		out = append(out, fmt.Sprintf("组件类型 %q 不在白名单中", n.Type))
	}
	if !slices.Contains(uiFormats, n.Format) {
		out = append(out, fmt.Sprintf("format %q 不受支持", n.Format))
	}
	for _, b := range []string{n.Bind, n.CurrencyBind, n.ValueBind, n.MaxBind, n.RowsBind} {
		if msg := checkBind(b, caps); msg != "" {
			out = append(out, msg)
		}
	}
	if n.Type == "link" {
		if u, err := url.Parse(n.Href); err != nil || u.Scheme != "https" || u.Host == "" {
			out = append(out, "link 只允许 https 地址")
		}
	}
	if n.Type == "alert" && !slices.Contains([]string{"info", "warning", "error", "success"}, n.Level) {
		out = append(out, "alert.level 必须是 info / warning / error / success")
	}
	if len(n.Text) > 2000 || len(n.Label) > 100 {
		out = append(out, "文本过长")
	}
	for _, c := range n.Columns {
		if !slices.Contains(uiFormats, c.Format) {
			out = append(out, fmt.Sprintf("列 %q 的 format 不受支持", c.Key))
		}
	}
	for _, it := range n.Items {
		out = append(out, checkNode(it, caps, depth+1)...)
	}
	return out
}

// Contributions decodes validated contributions for API responses.
func (m *Manifest) Contributions() []UIContribution {
	out := []UIContribution{}
	for _, r := range m.UIContributions {
		var c UIContribution
		if json.Unmarshal(r, &c) == nil {
			out = append(out, c)
		}
	}
	return out
}
