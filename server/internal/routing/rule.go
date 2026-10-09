// Package routing stores administrator route rules (docs/contracts/phase4-api.md
// §2) and implements the channel ordering strategies used by the gateway.
//
// Rules are kept in an in-memory snapshot (reloaded after local changes and
// every few seconds for other instances). Matching never touches the database.
package routing

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/identity"
)

// Strategies.
const (
	StrategyPriority     = "priority"
	StrategyWeighted     = "weighted"
	StrategyRoundRobin   = "round_robin"
	StrategyLeastLatency = "least_latency"
	StrategyLowestCost   = "lowest_cost"
)

// Protocol preferences.
const (
	PreferNative = "native_first"
	PreferIgnore = "ignore"
)

// Retry classes: failure types after which another channel may be tried.
// Upstream HTTP responses are classified by status code only.
const (
	RetryRateLimit   = "rate_limit"   // 429
	RetryServerError = "server_error" // 5xx, malformed upstream responses, plugin hook failures
	RetryTimeout     = "timeout"      // 408 and upstream timeouts
	RetryNetwork     = "network"      // connection and read errors
	RetryAuthError   = "auth_error"   // 401, 403
	RetryNotFound    = "not_found"    // 404
	RetryClientError = "client_error" // every other 4xx (400, 402, 409, 413, 422, …)
)

// AllRetryClasses lists every valid retry class.
var AllRetryClasses = []string{RetryRateLimit, RetryServerError, RetryTimeout, RetryNetwork, RetryAuthError, RetryNotFound, RetryClientError}

// DefaultRetryClasses is the default retryOn for new rules and for the
// gateway.retryOn setting: everything except other 4xx, which usually means
// the request itself is invalid.
var DefaultRetryClasses = []string{RetryRateLimit, RetryServerError, RetryTimeout, RetryNetwork, RetryAuthError, RetryNotFound}

var strategies = []string{StrategyPriority, StrategyWeighted, StrategyRoundRobin, StrategyLeastLatency, StrategyLowestCost}

// Limits from the contract.
const (
	MaxModels         = 100
	MaxTargets        = 50
	MaxFallbackModels = 5
	MaxRetryAttempts  = 5
	// HardAttemptCap bounds upstream attempts per request, fallbacks included.
	HardAttemptCap = 10

	defaultRuleAttempts = 3
	maxNameLen          = 100
	maxDescriptionLen   = 500
	maxModelLen         = 128
)

// Match selects the requests a rule applies to.
type Match struct {
	Models []string        `json:"models"`
	Roles  []identity.Role `json:"roles"`
}

// Target restricts (and optionally re-weights) one candidate channel.
type Target struct {
	ChannelID uuid.UUID `json:"channelId"`
	Priority  *int      `json:"priority"`
	Weight    *int      `json:"weight"`
}

// Retry is the per-rule retry policy.
type Retry struct {
	MaxAttempts int      `json:"maxAttempts"`
	RetryOn     []string `json:"retryOn"`
}

// Allows reports whether a failure of class may be retried on another channel.
func (r Retry) Allows(class string) bool { return class != "" && slices.Contains(r.RetryOn, class) }

// Rule is the API form of a route rule.
type Rule struct {
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	Enabled            bool      `json:"enabled"`
	Position           int       `json:"position"`
	Match              Match     `json:"match"`
	Targets            []Target  `json:"targets"`
	Strategy           string    `json:"strategy"`
	ProtocolPreference string    `json:"protocolPreference"`
	Retry              Retry     `json:"retry"`
	FallbackModels     []string  `json:"fallbackModels"`
	Version            int       `json:"version"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// spec is the JSON document stored in route_rules.spec.
type spec struct {
	Match              Match    `json:"match"`
	Targets            []Target `json:"targets"`
	Strategy           string   `json:"strategy"`
	ProtocolPreference string   `json:"protocolPreference"`
	Retry              Retry    `json:"retry"`
	FallbackModels     []string `json:"fallbackModels"`
}

func (r *Rule) spec() spec {
	return spec{Match: r.Match, Targets: r.Targets, Strategy: r.Strategy, ProtocolPreference: r.ProtocolPreference,
		Retry: r.Retry, FallbackModels: r.FallbackModels}
}

func (r *Rule) setSpec(s spec) {
	r.Match, r.Targets, r.Strategy, r.ProtocolPreference, r.Retry, r.FallbackModels =
		s.Match, s.Targets, s.Strategy, s.ProtocolPreference, s.Retry, s.FallbackModels
	r.normalize()
}

// normalize fills defaults and replaces nil slices (stable JSON).
func (r *Rule) normalize() {
	if r.Match.Models == nil {
		r.Match.Models = []string{}
	}
	if r.Match.Roles == nil {
		r.Match.Roles = []identity.Role{}
	}
	if r.Targets == nil {
		r.Targets = []Target{}
	}
	if r.FallbackModels == nil {
		r.FallbackModels = []string{}
	}
	if r.Strategy == "" {
		r.Strategy = StrategyPriority
	}
	if r.ProtocolPreference == "" {
		r.ProtocolPreference = PreferNative
	}
	if r.Retry.MaxAttempts == 0 {
		r.Retry.MaxAttempts = defaultRuleAttempts
	}
	if r.Retry.RetryOn == nil {
		r.Retry.RetryOn = slices.Clone(DefaultRetryClasses)
	}
}

// Matches reports whether the rule applies to a request for model by a user
// with role (the rule must also be enabled).
func (r *Rule) Matches(model string, role identity.Role) bool {
	if !r.Enabled {
		return false
	}
	if len(r.Match.Roles) > 0 && !slices.Contains(r.Match.Roles, role) {
		return false
	}
	for _, p := range r.Match.Models {
		if MatchModel(p, model) {
			return true
		}
	}
	return false
}

// MatchesAnyModel reports whether any of the rule's patterns matches model
// (ignoring enabled state and roles).
func (r *Rule) MatchesAnyModel(model string) bool {
	for _, p := range r.Match.Models {
		if MatchModel(p, model) {
			return true
		}
	}
	return false
}

// MatchModel matches a logical model name against an exact name or a glob in
// which '*' matches any (possibly empty) sequence of characters. No other
// character is special, so names containing '/', '?' or '[' match literally.
func MatchModel(pattern, model string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == model
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(model, parts[0]) {
		return false
	}
	rest := model[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}
		rest = rest[i+len(p):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// TargetFor returns the rule's target entry for a channel.
func (r *Rule) TargetFor(id uuid.UUID) (Target, bool) {
	for _, t := range r.Targets {
		if t.ChannelID == id {
			return t, true
		}
	}
	return Target{}, false
}

// RetryInput is the writable form of Retry (nil = keep / default).
type RetryInput struct {
	MaxAttempts *int     `json:"maxAttempts"`
	RetryOn     []string `json:"retryOn"`
}

// Input is the body of create (all optional except name and match) and
// update (nil = unchanged; version required).
type Input struct {
	Name               *string     `json:"name"`
	Description        *string     `json:"description"`
	Enabled            *bool       `json:"enabled"`
	Match              *Match      `json:"match"`
	Targets            *[]Target   `json:"targets"`
	Strategy           *string     `json:"strategy"`
	ProtocolPreference *string     `json:"protocolPreference"`
	Retry              *RetryInput `json:"retry"`
	FallbackModels     *[]string   `json:"fallbackModels"`
	Version            *int        `json:"version"`
}

// apply validates in onto r; it returns validation details (empty when valid).
func (in *Input) apply(r *Rule, create bool) map[string]any {
	details := map[string]any{}
	if create {
		if in.Name == nil {
			details["name"] = "必填"
		}
		if in.Match == nil {
			details["match.models"] = "必填"
		}
	} else if in.Version == nil {
		details["version"] = "必填"
	}
	if in.Name != nil {
		r.Name = strings.TrimSpace(*in.Name)
		if n := len([]rune(r.Name)); n < 1 || n > maxNameLen {
			details["name"] = fmt.Sprintf("长度应为 1–%d 个字符", maxNameLen)
		}
	}
	if in.Description != nil {
		r.Description = strings.TrimSpace(*in.Description)
		if len([]rune(r.Description)) > maxDescriptionLen {
			details["description"] = fmt.Sprintf("不能超过 %d 个字符", maxDescriptionLen)
		}
	}
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	if in.Match != nil {
		r.Match = Match{Models: dedupe(trimAll(in.Match.Models)), Roles: dedupe(in.Match.Roles)}
		if n := len(r.Match.Models); n < 1 || n > MaxModels {
			details["match.models"] = fmt.Sprintf("需要 1–%d 个模型", MaxModels)
		}
		for _, m := range r.Match.Models {
			if m == "" || len(m) > maxModelLen {
				details["match.models"] = fmt.Sprintf("模型名长度应为 1–%d 个字符", maxModelLen)
			}
		}
		for _, role := range r.Match.Roles {
			if !role.Valid() {
				details["match.roles"] = "包含无效的角色"
			}
		}
	}
	if in.Targets != nil {
		r.Targets = *in.Targets
		if len(r.Targets) > MaxTargets {
			details["targets"] = fmt.Sprintf("最多 %d 个渠道", MaxTargets)
		}
		seen := map[uuid.UUID]bool{}
		for i, t := range r.Targets {
			if seen[t.ChannelID] {
				details[fmt.Sprintf("targets[%d].channelId", i)] = "渠道重复"
			}
			seen[t.ChannelID] = true
			if t.Weight != nil && (*t.Weight < 1 || *t.Weight > 1000) {
				details[fmt.Sprintf("targets[%d].weight", i)] = "应为 1–1000，或 null 沿用渠道设置"
			}
			if t.Priority != nil && (*t.Priority < -1000000 || *t.Priority > 1000000) {
				details[fmt.Sprintf("targets[%d].priority", i)] = "超出范围"
			}
		}
	}
	if in.Strategy != nil {
		r.Strategy = *in.Strategy
		if !slices.Contains(strategies, r.Strategy) {
			details["strategy"] = "只能是 priority、weighted、round_robin、least_latency 或 lowest_cost"
		}
	}
	if in.ProtocolPreference != nil {
		r.ProtocolPreference = *in.ProtocolPreference
		if r.ProtocolPreference != PreferNative && r.ProtocolPreference != PreferIgnore {
			details["protocolPreference"] = "只能是 native_first 或 ignore"
		}
	}
	if in.Retry != nil {
		if in.Retry.MaxAttempts != nil {
			r.Retry.MaxAttempts = *in.Retry.MaxAttempts
			if r.Retry.MaxAttempts < 1 || r.Retry.MaxAttempts > MaxRetryAttempts {
				details["retry.maxAttempts"] = fmt.Sprintf("应为 1–%d", MaxRetryAttempts)
			}
		}
		if in.Retry.RetryOn != nil {
			r.Retry.RetryOn = dedupe(in.Retry.RetryOn)
			for _, c := range r.Retry.RetryOn {
				if !slices.Contains(AllRetryClasses, c) {
					details["retry.retryOn"] = "只能包含 " + strings.Join(AllRetryClasses, "、")
				}
			}
		}
	}
	if in.FallbackModels != nil {
		r.FallbackModels = dedupe(trimAll(*in.FallbackModels))
		if len(r.FallbackModels) > MaxFallbackModels {
			details["fallbackModels"] = fmt.Sprintf("最多 %d 个", MaxFallbackModels)
		}
		for _, m := range r.FallbackModels {
			if m == "" || len(m) > maxModelLen || strings.Contains(m, "*") {
				details["fallbackModels"] = "必须是精确的逻辑模型名（不能使用通配符）"
			}
		}
	}
	r.normalize()
	return details
}

func trimAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.TrimSpace(s)
	}
	return out
}

func dedupe[T comparable](in []T) []T {
	out := make([]T, 0, len(in))
	seen := map[T]bool{}
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
