// Package channel manages upstream channels: configuration, encrypted
// credentials, model mapping, visibility (private/shared/global), health and
// the in-memory runtime snapshot used by the data plane.
package channel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/money"
	"omnigate/internal/protocol"
)

const (
	TypeOpenAI    = "openai"
	TypeAnthropic = "anthropic"
	// TypeCustom channels speak an upstream protocol implemented by their
	// plugin (manifest protocol "custom", phase9-api.md §2); the type is set
	// from the plugin and cannot be chosen without one.
	TypeCustom = "custom"

	secretAPIKey = "api_key"
)

type ModelMap struct {
	Model         string `json:"model"`
	UpstreamModel string `json:"upstreamModel"`
	// UpstreamProtocol (openai channels): "" or "chat" = Chat Completions,
	// "responses" = OpenAI Responses API.
	UpstreamProtocol string `json:"upstreamProtocol,omitempty"`
}

type Config struct {
	Headers           map[string]string `json:"headers,omitempty"`
	SupportsResponses bool              `json:"supportsResponses,omitempty"`
	// SupportsCompletions (openai channels) declares that the upstream serves
	// the legacy /completions endpoint (FIM via suffix, phase14-api.md):
	// only such channels are candidates for /v1/completions. Ignored on
	// anthropic and custom channels.
	SupportsCompletions bool `json:"supportsCompletions,omitempty"`
	TimeoutSeconds      int  `json:"timeoutSeconds,omitempty"`
	// MaxTokensField selects how converted requests to an openai channel carry
	// the output limit: "max_tokens" (default) or "max_completion_tokens".
	MaxTokensField string `json:"maxTokensField,omitempty"`
}

type SecretInfo struct {
	Set  bool    `json:"set"`
	Hint *string `json:"hint"`
}

type OwnerRef struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"displayName"`
}

type Health struct {
	State               string     `json:"state"` // healthy | degraded | open
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	LastError           *string    `json:"lastError"`
	LastCheckedAt       *time.Time `json:"lastCheckedAt"`
}

// Channel is the full (owner/admin) view.
type Channel struct {
	ID         uuid.UUID   `json:"id"`
	Name       string      `json:"name"`
	Type       string      `json:"type"`
	BaseURL    string      `json:"baseUrl"`
	Scope      authz.Scope `json:"scope"`
	SharedWith Sharing     `json:"sharedWith"`
	Status     string      `json:"status"`
	Priority   int         `json:"priority"`
	Weight     int         `json:"weight"`
	Models     []ModelMap  `json:"models"`
	Config     Config      `json:"config"`
	Secret     SecretInfo  `json:"secret"`
	Owner      OwnerRef    `json:"owner"`
	Health     Health      `json:"health"`
	Version    int         `json:"version"`
	CreatedAt  time.Time   `json:"createdAt"`
	UpdatedAt  time.Time   `json:"updatedAt"`

	PluginVersionID *uuid.UUID            `json:"-"`
	Plugin          *PluginRef            `json:"plugin"`
	PluginConfig    map[string]any        `json:"pluginConfig"`
	SecretFields    map[string]SecretInfo `json:"secretFields"`
	Badges          []Badge               `json:"badges,omitempty"`
	Alerts          Alerts                `json:"alerts"`
	// Shares lists every invited user with the invitation status (owner /
	// channels.manage view, docs/contracts/phase5-api.md §5.2).
	Shares []ChannelShare `json:"shares"`

	// invited are the users that received a new invitation in the last
	// Create / Update (set by the store inside the transaction).
	invited []uuid.UUID
}

// Share statuses of user-to-user shares (phase5-api.md §5): only accepted
// shares make a channel usable by the recipient.
const (
	SharePending  = "pending"
	ShareAccepted = "accepted"
	ShareDeclined = "declined"
)

// ChannelShare is one invited user of a shared channel.
type ChannelShare struct {
	UserID      uuid.UUID  `json:"userId"`
	DisplayName string     `json:"displayName"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	RespondedAt *time.Time `json:"respondedAt"`
}

// acceptedUsers returns the users that accepted their share.
func (c *Channel) acceptedUsers() []uuid.UUID {
	var out []uuid.UUID
	for _, s := range c.Shares {
		if s.Status == ShareAccepted {
			out = append(out, s.UserID)
		}
	}
	return out
}

// Alerts are a channel's alert settings (docs/contracts/phase6-api.md §5).
type Alerts struct {
	// BalanceBelow is the upstream balance threshold (decimal text in the
	// plugin's balance currency); nil = no balance alert.
	BalanceBelow *string `json:"balanceBelow"`
}

// PluginRef identifies the plugin version a channel is pinned to.
type PluginRef struct {
	ID        uuid.UUID `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	VersionID uuid.UUID `json:"versionId"`
}

// Badge is a resolved channel.list.badge contribution.
type Badge struct {
	Value    any    `json:"value"`
	Format   string `json:"format,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// Summary is what users who may only *use* a channel can see.
type Summary struct {
	ID     uuid.UUID   `json:"id"`
	Name   string      `json:"name"`
	Type   string      `json:"type"`
	Scope  authz.Scope `json:"scope"`
	Status string      `json:"status"`
	Models []ModelMap  `json:"models"`
	Health Health      `json:"health"`
	Plugin *PluginRef  `json:"plugin"`
}

func (c *Channel) Summary() Summary {
	return Summary{ID: c.ID, Name: c.Name, Type: c.Type, Scope: c.Scope, Status: c.Status, Models: c.Models, Health: c.Health, Plugin: c.Plugin}
}

// Sharing lists who a shared channel is shared with: individual users and
// whole user groups (phase8-api.md §1.2). Users are the invited users
// (pending or accepted, phase5-api.md §5.2); only accepted ones may use it.
type Sharing struct {
	Users  []uuid.UUID `json:"users"`
	Groups []uuid.UUID `json:"groups"`
}

// SharingInput is the writable sharedWith: {users, groups}, or (compatible
// with earlier clients) an array of user ids.
type SharingInput Sharing

// UnmarshalJSON accepts an object or an array of user ids.
func (s *SharingInput) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*s = SharingInput{}
	if len(b) > 0 && b[0] == '[' {
		return json.Unmarshal(b, &s.Users)
	}
	if string(b) == "null" {
		return nil
	}
	var v struct {
		Users  []uuid.UUID `json:"users"`
		Groups []uuid.UUID `json:"groups"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	s.Users, s.Groups = v.Users, v.Groups
	return nil
}

// Resource adapts the channel for authz checks. viewerGroup is the group of
// the user being checked (nil when unknown). Only accepted user shares count.
func (c *Channel) Resource(viewerGroup *uuid.UUID) authz.Resource {
	shared := map[uuid.UUID]bool{}
	for _, id := range c.acceptedUsers() {
		shared[id] = true
	}
	inGroup := false
	if viewerGroup != nil {
		for _, g := range c.SharedWith.Groups {
			inGroup = inGroup || g == *viewerGroup
		}
	}
	return authz.Resource{OwnerID: c.Owner.ID, Scope: c.Scope, SharedTo: func(u uuid.UUID, _ identity.Role) bool { return shared[u] || inGroup }}
}

// Input is the writable subset used by create and update. Nil pointers mean
// "unchanged" on update; create requires the mandatory ones.
type Input struct {
	Name       *string       `json:"name"`
	Type       *string       `json:"type"`
	BaseURL    *string       `json:"baseUrl"`
	Scope      *authz.Scope  `json:"scope"`
	SharedWith *SharingInput `json:"sharedWith"`
	Status     *string       `json:"status"`
	Priority   *int          `json:"priority"`
	Weight     *int          `json:"weight"`
	Models     *[]ModelMap   `json:"models"`
	Config     *Config       `json:"config"`
	APIKey     *string       `json:"apiKey"`
	Version    *int          `json:"version"`

	PluginVersionID *uuid.UUID         `json:"pluginVersionId"`
	PluginConfig    *map[string]any    `json:"pluginConfig"`
	Secrets         *map[string]string `json:"secrets"`
	Alerts          *Alerts            `json:"alerts"`

	// groupsAllowed: the caller holds channels.manage and may add group
	// shares (phase5-api.md §5.2); set by the service.
	groupsAllowed bool
}

var forbiddenHeaders = map[string]bool{
	"authorization": true, "x-api-key": true, "host": true, "content-length": true, "connection": true,
	"transfer-encoding": true, "cookie": true, "te": true, "upgrade": true, "keep-alive": true,
	"proxy-authorization": true, "proxy-connection": true, "content-type": true, "accept-encoding": true,
}

// apply validates in and applies it onto c. create=true enforces required fields.
func (in *Input) apply(c *Channel, create bool) error {
	details := map[string]any{}
	if create {
		for field, missing := range map[string]bool{"name": in.Name == nil, "type": in.Type == nil, "baseUrl": in.BaseURL == nil, "apiKey": in.APIKey == nil} {
			if missing {
				details[field] = "必填"
			}
		}
	} else if in.Version == nil {
		details["version"] = "必填"
	}
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
		if c.Name == "" || len([]rune(c.Name)) > 64 {
			details["name"] = "长度应为 1–64 个字符"
		}
	}
	if in.Type != nil {
		if !create && *in.Type != c.Type {
			details["type"] = "渠道类型创建后不能修改"
		}
		c.Type = *in.Type
		if c.Type != TypeOpenAI && c.Type != TypeAnthropic && c.Type != TypeCustom {
			details["type"] = "仅支持 openai 或 anthropic（custom 由自定义协议插件决定）"
		}
	}
	if in.BaseURL != nil {
		u, err := normalizeBaseURL(*in.BaseURL)
		if err != nil {
			details["baseUrl"] = err.Error()
		}
		c.BaseURL = u
	}
	if in.Scope != nil {
		c.Scope = *in.Scope
		if c.Scope != authz.ScopePrivate && c.Scope != authz.ScopeShared && c.Scope != authz.ScopeGlobal {
			details["scope"] = "无效的作用域"
		}
	}
	if in.SharedWith != nil {
		had := map[uuid.UUID]bool{}
		for _, g := range c.SharedWith.Groups {
			had[g] = true
		}
		c.SharedWith = Sharing{Users: dedupe(in.SharedWith.Users), Groups: dedupe(in.SharedWith.Groups)}
		for _, g := range c.SharedWith.Groups {
			if !had[g] && !in.groupsAllowed {
				details["sharedWith.groups"] = "只有渠道管理员可以共享给用户组"
			}
		}
		if len(c.SharedWith.Users) > 200 {
			details["sharedWith"] = "最多共享给 200 个用户"
		}
		if len(c.SharedWith.Groups) > 50 {
			details["sharedWith.groups"] = "最多共享给 50 个用户组"
		}
	}
	if c.Scope != authz.ScopeShared {
		c.SharedWith = Sharing{}
	}
	if c.SharedWith.Users == nil {
		c.SharedWith.Users = []uuid.UUID{}
	}
	if c.SharedWith.Groups == nil {
		c.SharedWith.Groups = []uuid.UUID{}
	}
	if in.Status != nil {
		c.Status = *in.Status
		if c.Status != "enabled" && c.Status != "disabled" {
			details["status"] = "无效的状态"
		}
	}
	if in.Priority != nil {
		c.Priority = *in.Priority
		if c.Priority < -1000 || c.Priority > 1000 {
			details["priority"] = "范围为 -1000 到 1000"
		}
	}
	if in.Weight != nil {
		c.Weight = *in.Weight
		if c.Weight < 1 || c.Weight > 1000 {
			details["weight"] = "范围为 1 到 1000"
		}
	}
	if in.Models != nil {
		c.Models = nil
		seen := map[string]bool{}
		for _, m := range *in.Models {
			m.Model, m.UpstreamModel = strings.TrimSpace(m.Model), strings.TrimSpace(m.UpstreamModel)
			if m.UpstreamModel == "" {
				m.UpstreamModel = m.Model
			}
			if m.Model == "" || len(m.Model) > 128 || len(m.UpstreamModel) > 128 {
				details["models"] = "模型名长度应为 1–128 个字符"
				break
			}
			if seen[m.Model] {
				details["models"] = fmt.Sprintf("模型 %q 重复", m.Model)
				break
			}
			if m.UpstreamProtocol == "chat" {
				m.UpstreamProtocol = ""
			}
			if m.UpstreamProtocol != "" && m.UpstreamProtocol != "responses" {
				details["models"] = fmt.Sprintf("模型 %q 的 upstreamProtocol 只能是 chat 或 responses", m.Model)
				break
			}
			if m.UpstreamProtocol != "" && c.Type != TypeOpenAI {
				details["models"] = fmt.Sprintf("模型 %q：只有 OpenAI 兼容渠道可以设置上游协议", m.Model)
				break
			}
			seen[m.Model] = true
			c.Models = append(c.Models, m)
		}
		if len(c.Models) > 500 {
			details["models"] = "最多 500 个模型"
		}
	}
	if c.Models == nil {
		c.Models = []ModelMap{}
	}
	if (create || in.Models != nil) && len(c.Models) == 0 {
		details["models"] = "至少配置一个模型"
	}
	if in.Config != nil {
		cfg := *in.Config
		if len(cfg.Headers) > 20 {
			details["config.headers"] = "最多 20 个自定义请求头"
		}
		for k, v := range cfg.Headers {
			if !validHeaderName(k) || forbiddenHeaders[strings.ToLower(k)] || strings.ContainsAny(v, "\r\n") {
				details["config.headers"] = fmt.Sprintf("请求头 %q 不允许或格式无效", k)
			}
		}
		if cfg.TimeoutSeconds < 0 || cfg.TimeoutSeconds > 600 {
			details["config.timeoutSeconds"] = "范围为 0–600 秒（0 表示默认 60 秒）"
		}
		if cfg.MaxTokensField != "" && cfg.MaxTokensField != "max_tokens" && cfg.MaxTokensField != "max_completion_tokens" {
			details["config.maxTokensField"] = "只能是 max_tokens 或 max_completion_tokens"
		}
		c.Config = cfg
	}
	if in.Alerts != nil {
		c.Alerts = Alerts{}
		if b := in.Alerts.BalanceBelow; b != nil && strings.TrimSpace(*b) != "" {
			a, err := money.Parse(strings.TrimSpace(*b))
			if err != nil || a < 0 {
				details["alerts.balanceBelow"] = "必须为空或不小于 0 的十进制金额（最多 9 位小数）"
			} else {
				v := a.String()
				c.Alerts.BalanceBelow = &v
			}
		}
	}
	if in.APIKey != nil && (strings.TrimSpace(*in.APIKey) == "" || len(*in.APIKey) > 4096 || strings.ContainsAny(*in.APIKey, "\r\n")) {
		details["apiKey"] = "API Key 不能为空、不能包含换行，且不超过 4096 字符"
	}
	if len(details) > 0 {
		return apperr.Validation("渠道参数校验失败", details)
	}
	return nil
}

func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("必须是 http(s) 绝对地址")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("不能包含用户信息、查询参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

// ValidHeaderName reports whether s is an acceptable header name for channel
// configuration and session affinity pass_headers: 1–64 characters of
// letters, digits, '-' and '_'.
func ValidHeaderName(s string) bool { return validHeaderName(s) }

// ForbiddenHeader reports whether a header may never be set from
// configuration or copied from a client (credentials, cookies, hop-by-hop and
// framing headers).
func ForbiddenHeader(name string) bool { return forbiddenHeaders[strings.ToLower(name)] }

func validHeaderName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func dedupe(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Dialect returns the upstream protocol the channel uses for a client dialect
// and logical model. Every combination is reachable: the gateway converts
// between Chat, Responses and Messages as needed.
//
//	anthropic channel: always Anthropic Messages
//	openai channel:    the model's upstreamProtocol "responses" -> Responses;
//	                   Responses clients -> Responses passthrough when the channel
//	                   declares supportsResponses, otherwise converted to Chat;
//	                   everything else -> Chat Completions
//	custom channel:    protocol.Custom (the plugin speaks Chat Completions to
//	                   the gateway; every client protocol needs one conversion)
func (c *Channel) Dialect(client, model string) string {
	if protocol.OpenAIOnly(client) {
		return client // embeddings, images, audio, completions: only routed to openai channels (see Supports)
	}
	if c.Type == TypeCustom {
		return protocol.Custom
	}
	if c.Type == TypeAnthropic {
		return protocol.Anthropic
	}
	for _, m := range c.Models {
		if m.Model == model && m.UpstreamProtocol == "responses" {
			return protocol.OpenAIResponses
		}
	}
	if client == protocol.OpenAIResponses && c.Config.SupportsResponses {
		return protocol.OpenAIResponses
	}
	return protocol.OpenAIChat
}

// Supports reports whether the channel can serve a client dialect at all:
// embeddings, the image and the audio endpoints are served by openai channels
// only, completions by openai channels that declare supportsCompletions
// (custom channels serve Chat, Messages and Responses clients).
func (c *Channel) Supports(client string) bool {
	if client == protocol.OpenAICompletions {
		return c.Type == TypeOpenAI && c.Config.SupportsCompletions
	}
	return !protocol.OpenAIOnly(client) || c.Type == TypeOpenAI
}
