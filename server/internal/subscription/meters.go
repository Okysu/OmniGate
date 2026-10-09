package subscription

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
	"omnigate/internal/protocol"
)

// Custom meters (phase9-api.md §3): a rule's meter "custom:<pluginKey>.<meter>"
// is evaluated by a billing plugin's computeUnits when usage is recorded.

const customPrefix = "custom:"

var (
	meterPluginRe = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9-]+)+$`)
	meterNameRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
)

// CustomMeterRef splits "custom:<pluginKey>.<meter>".
func CustomMeterRef(meter string) (pluginKey, name string, ok bool) {
	ref, found := strings.CutPrefix(meter, customPrefix)
	if !found {
		return "", "", false
	}
	i := strings.LastIndex(ref, ".")
	if i <= 0 {
		return "", "", false
	}
	pluginKey, name = ref[:i], ref[i+1:]
	return pluginKey, name, len(pluginKey) <= 64 && meterPluginRe.MatchString(pluginKey) && meterNameRe.MatchString(name)
}

func isCustomMeter(meter string) bool { return strings.HasPrefix(meter, customPrefix) }

// BillingCtx is what a custom meter learns about the request (BillingCtx of
// phase9-api.md §3).
type BillingCtx struct {
	Model        string
	ServedModel  string
	ChannelID    string
	ChannelTier  string
	UserGroup    string
	Inbound      string
	ImageCount   int64
	AudioSeconds float64
}

// CustomMeters evaluates plugin meters (implemented by the plugin service).
type CustomMeters interface {
	// ResolveMeter returns the newest approved version of the enabled plugin
	// pluginKey that declares meter, or a user-facing problem ("" = found).
	ResolveMeter(ctx context.Context, pluginKey, meter string) (ResolvedMeter, string, error)
	// Units evaluates the meter of the pinned version. Failures (unavailable
	// plugin, timeout, exception, invalid result) yield "0" and are logged and
	// counted by the implementation.
	Units(ctx context.Context, versionID uuid.UUID, pluginKey, meter string, usage protocol.Usage, bc BillingCtx) string
	// Options lists the custom meters that rules can select.
	Options(ctx context.Context) ([]MeterOption, error)
}

// ResolvedMeter is the plugin version a custom meter is pinned to.
type ResolvedMeter struct {
	VersionID uuid.UUID
	Label     string
	Unit      string
}

// MeterOption is a meter a quota rule can use (GET /api/admin/billing/meters).
type MeterOption struct {
	Meter   string       `json:"meter"`
	Label   string       `json:"label"`
	Unit    string       `json:"unit,omitempty"`
	Builtin bool         `json:"builtin"`
	Plugin  *MeterPlugin `json:"plugin"`
	Integer bool         `json:"integer"` // the rule limit must be an integer
}

// MeterPlugin identifies the plugin version that provides a custom meter.
type MeterPlugin struct {
	ID        uuid.UUID `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	VersionID uuid.UUID `json:"versionId"`
}

var builtinMeters = []MeterOption{
	{Meter: MeterRequests, Label: "请求数", Unit: "次", Builtin: true, Integer: true},
	{Meter: MeterTokensInput, Label: "输入 token", Unit: "token", Builtin: true, Integer: true},
	{Meter: MeterTokensOutput, Label: "输出 token", Unit: "token", Builtin: true, Integer: true},
	{Meter: MeterTokensTotal, Label: "总 token", Unit: "token", Builtin: true, Integer: true},
	{Meter: MeterImages, Label: "图片张数", Unit: "张", Builtin: true, Integer: true},
	{Meter: MeterAudioSeconds, Label: "音频秒数", Unit: "秒", Builtin: true, Integer: true},
	{Meter: MeterCharge, Label: "费用", Builtin: true},
}

// Meters lists the built-in meters and the custom meters of enabled billing
// plugins (latest approved version).
func (s *Service) Meters(ctx context.Context) ([]MeterOption, error) {
	out := append([]MeterOption{}, builtinMeters...)
	if s.CustomMeters == nil {
		return out, nil
	}
	custom, err := s.CustomMeters.Options(ctx)
	if err != nil {
		return nil, err
	}
	return append(out, custom...), nil
}

// resolveCustomMeters validates the custom meters of rules (plugin enabled,
// approved version declaring the meter) and pins each to the newest such
// version; other rules lose any client-sent pin. Field errors go to details.
func (s *Service) resolveCustomMeters(ctx context.Context, rules []Rule, details map[string]any) error {
	for i := range rules {
		r := &rules[i]
		r.PluginVersionID, r.MeterLabel, r.MeterUnit = nil, "", ""
		key, name, ok := CustomMeterRef(strings.TrimSpace(r.Meter))
		if !ok {
			continue // built-in, or reported by validateRules
		}
		field := fmt.Sprintf("rules[%d].meter", i)
		if s.CustomMeters == nil {
			details[field] = "插件计量不可用"
			continue
		}
		res, problem, err := s.CustomMeters.ResolveMeter(ctx, key, name)
		if err != nil {
			return err
		}
		if problem != "" {
			details[field] = problem
			continue
		}
		r.PluginVersionID, r.MeterLabel, r.MeterUnit = &res.VersionID, res.Label, res.Unit
	}
	return nil
}

// RecordInput is one finished request covered by a subscription.
type RecordInput struct {
	SubscriptionID uuid.UUID
	RequestID      string
	// Model is the model that served the request (rules apply to it).
	Model   string
	Usage   protocol.Usage
	Charge  money.Amount
	At      time.Time
	Billing BillingCtx
}

// customUnits evaluates the custom meters of the subscription's rules that
// apply to in.Model, before the settlement transaction (plugin code never runs
// while the subscription row is locked). Rule weights are applied later.
func (s *Service) customUnits(ctx context.Context, in RecordInput) (map[string]Dec, error) {
	sub, err := getSub(ctx, s.pool, in.SubscriptionID, false)
	if err != nil {
		return nil, err
	}
	var out map[string]Dec
	for _, r := range sub.Rules {
		key, name, ok := CustomMeterRef(r.Meter)
		if !ok || !covers(r.Models, in.Model) {
			continue
		}
		if out == nil {
			out = map[string]Dec{}
		}
		if s.CustomMeters == nil || r.PluginVersionID == nil {
			s.log.WarnContext(ctx, "custom meter unavailable: counted as 0", "subscription", sub.ID, "rule", r.ID, "meter", r.Meter)
			out[r.ID] = Dec{}
			continue
		}
		units := s.CustomMeters.Units(ctx, *r.PluginVersionID, key, name, in.Usage, in.Billing)
		d, err := ParseDec(units)
		if err != nil || d.Sign() < 0 {
			s.log.WarnContext(ctx, "custom meter returned an invalid value: counted as 0", "rule", r.ID, "meter", r.Meter, "units", units)
			d = Dec{}
		}
		out[r.ID] = d
	}
	return out, nil
}

// weighted applies the rule's model weight to custom units.
func (c *compiledRule) weighted(model string, v Dec) Dec {
	if w, ok := c.weights[model]; ok {
		v = v.Mul(w)
	}
	if v.Sign() < 0 {
		return Dec{}
	}
	return v
}
