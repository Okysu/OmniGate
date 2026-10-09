package subscription

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"omnigate/internal/money"
	"omnigate/internal/protocol"
)

// Meters (contracts/phase3-api.md §1).
const (
	MeterRequests     = "requests"
	MeterTokensInput  = "tokens.input"
	MeterTokensOutput = "tokens.output"
	MeterTokensTotal  = "tokens.total"
	MeterCharge       = "charge"
	// MeterImages counts output images of the image endpoints (phase7-api.md §1.1).
	MeterImages = "images"
	// MeterAudioSeconds counts billed seconds of input audio of the audio
	// endpoints (phase9-api.md §1.1).
	MeterAudioSeconds = "audio_seconds"
)

// Window kinds.
const (
	WindowCalendar = "calendar"
	WindowRolling  = "rolling"
	WindowSession  = "session"
	WindowPeriod   = "period"
	WindowLifetime = "lifetime"
)

// What happens when every covering subscription is out of quota
// (billing_preferences.quota_overflow, overridable per API key).
const (
	OverflowBlock  = "block"
	OverflowWallet = "wallet"
)

// Limits on plan documents.
const (
	maxRules        = 10
	maxModels       = 200
	maxModelLen     = 200
	maxLabelLen     = 64
	maxWeight       = "1000"
	rollingBucket   = 5 * time.Minute
	defaultTimezone = "UTC"
)

var ruleIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// Window is the JSON form of a quota window. Only the fields of its kind are set.
type Window struct {
	Kind     string `json:"kind"`
	Unit     string `json:"unit,omitempty"`     // calendar: day | week | month
	Timezone string `json:"timezone,omitempty"` // calendar: IANA name, default UTC
	Duration string `json:"duration,omitempty"` // rolling, session
	Every    string `json:"every,omitempty"`    // period
}

// Rule is the JSON form of a quota rule, as stored in plans.rules and in
// subscription snapshots.
type Rule struct {
	ID           string            `json:"id"`
	Label        string            `json:"label"`
	Meter        string            `json:"meter"`
	Window       Window            `json:"window"`
	Limit        string            `json:"limit"`
	Models       []string          `json:"models"`
	ModelWeights map[string]string `json:"modelWeights"`
	// PluginVersionID pins the billing plugin version of a custom meter
	// ("custom:<pluginKey>.<meter>", phase9-api.md §3). It is resolved when
	// the plan's rules are saved (newest approved version declaring the
	// meter) and kept in subscription snapshots; ignored on input.
	PluginVersionID *uuid.UUID `json:"pluginVersionId,omitempty"`
	// MeterLabel / MeterUnit snapshot the custom meter's label and unit
	// from the pinned version's manifest (read-only, set with the pin), so
	// catalog and subscription pages can show them without plugin access.
	MeterLabel string `json:"meterLabel,omitempty"`
	MeterUnit  string `json:"meterUnit,omitempty"`
}

// DisplayName is the label, falling back to the id.
func (r Rule) DisplayName() string {
	if r.Label != "" {
		return r.Label
	}
	return r.ID
}

// normalize fills defaults so stored documents are explicit.
func (r *Rule) normalize() {
	r.ID = strings.TrimSpace(r.ID)
	r.Label = strings.TrimSpace(r.Label)
	if r.Window.Kind == WindowCalendar && r.Window.Timezone == "" {
		r.Window.Timezone = defaultTimezone
	}
	if r.Models == nil {
		r.Models = []string{}
	}
	if r.ModelWeights == nil {
		r.ModelWeights = map[string]string{}
	}
}

// compiledRule is a validated rule ready for window and meter math.
type compiledRule struct {
	Rule
	limit   Dec
	weights map[string]Dec
	dur     time.Duration // rolling/session duration or period length
	loc     *time.Location
}

// validateRules normalizes and validates rules in place, writing field errors to
// details under prefix (e.g. "rules[0].limit").
func validateRules(rules []Rule, details map[string]any) {
	if len(rules) < 1 || len(rules) > maxRules {
		details["rules"] = fmt.Sprintf("必须有 1 到 %d 条规则", maxRules)
		return
	}
	seen := map[string]bool{}
	for i := range rules {
		rules[i].normalize()
		p := fmt.Sprintf("rules[%d].", i)
		_, _ = compileRule(rules[i], details, p)
		if id := rules[i].ID; id != "" && seen[id] {
			details[p+"id"] = "规则 id 在套餐内必须唯一"
		}
		seen[rules[i].ID] = true
	}
}

type invalidRule struct{}

func (invalidRule) Error() string { return "invalid rule" }

// compileRule validates r. With details == nil it only reports whether the rule
// is valid (used for stored snapshots).
func compileRule(r Rule, details map[string]any, p string) (*compiledRule, error) {
	bad := false
	fail := func(field, msg string) {
		bad = true
		if details != nil {
			details[p+field] = msg
		}
	}
	c := &compiledRule{Rule: r, weights: map[string]Dec{}}
	if !ruleIDPattern.MatchString(r.ID) {
		fail("id", "只能包含小写字母、数字、- 和 _，长度 1–32")
	}
	if utf8.RuneCountInString(r.Label) > maxLabelLen {
		fail("label", fmt.Sprintf("不能超过 %d 个字符", maxLabelLen))
	}
	switch r.Meter {
	case MeterRequests, MeterTokensInput, MeterTokensOutput, MeterTokensTotal, MeterCharge, MeterImages, MeterAudioSeconds:
	default:
		if _, _, ok := CustomMeterRef(r.Meter); !ok {
			fail("meter", "必须为 requests、tokens.input、tokens.output、tokens.total、images、audio_seconds、charge 或 custom:<插件 ID>.<计量名>")
		}
	}
	w := r.Window
	extra := func(name, val string) {
		if val != "" {
			fail("window."+name, w.Kind+" 窗口不支持该字段")
		}
	}
	switch w.Kind {
	case WindowCalendar:
		switch w.Unit {
		case "day", "week", "month":
		default:
			fail("window.unit", "必须为 day、week 或 month")
		}
		loc, err := loadLocation(w.Timezone)
		if err != nil {
			fail("window.timezone", "不是合法的 IANA 时区")
		}
		c.loc = loc
		extra("duration", w.Duration)
		extra("every", w.Every)
	case WindowRolling, WindowSession:
		d, err := ParseWindowDuration(w.Duration)
		if err != nil {
			fail("window.duration", err.Error())
		}
		c.dur = d
		extra("unit", w.Unit)
		extra("timezone", w.Timezone)
		extra("every", w.Every)
	case WindowPeriod:
		d, err := ParsePeriod(w.Every)
		if err != nil {
			fail("window.every", err.Error())
		}
		c.dur = d
		extra("unit", w.Unit)
		extra("timezone", w.Timezone)
		extra("duration", w.Duration)
	case WindowLifetime:
		extra("unit", w.Unit)
		extra("timezone", w.Timezone)
		extra("duration", w.Duration)
		extra("every", w.Every)
	default:
		fail("window.kind", "必须为 calendar、rolling、session、period 或 lifetime")
	}
	limit, err := ParseDec(r.Limit)
	switch {
	case r.Limit == "":
		fail("limit", "必填")
	case err != nil:
		fail("limit", err.Error())
	case limit.Sign() <= 0:
		fail("limit", "必须大于 0")
	case r.Meter != MeterCharge && !isCustomMeter(r.Meter) && !limit.IsInt():
		fail("limit", "requests、tokens 与 images 计量的上限必须为整数")
	}
	c.limit = limit
	if msg := validateModels(r.Models); msg != "" {
		fail("models", msg)
	}
	if len(r.ModelWeights) > maxModels {
		fail("modelWeights", fmt.Sprintf("最多 %d 项", maxModels))
	}
	maxW := MustDec(maxWeight)
	for m, v := range r.ModelWeights {
		if m == "" || utf8.RuneCountInString(m) > maxModelLen {
			fail("modelWeights", "模型名不能为空且不能超过 200 个字符")
			continue
		}
		wt, err := ParseDec(v)
		if err != nil || wt.Sign() < 0 || wt.Cmp(maxW) > 0 {
			fail("modelWeights."+m, "必须是 0 到 1000 之间的十进制数")
			continue
		}
		c.weights[m] = wt
	}
	if bad {
		return nil, invalidRule{}
	}
	return c, nil
}

// validateModels checks a model list; it returns "" when valid.
func validateModels(models []string) string {
	if len(models) > maxModels {
		return fmt.Sprintf("最多 %d 个模型", maxModels)
	}
	seen := map[string]bool{}
	for _, m := range models {
		if strings.TrimSpace(m) != m || m == "" || utf8.RuneCountInString(m) > maxModelLen {
			return "模型名不能为空、不能有首尾空格且不能超过 200 个字符"
		}
		if seen[m] {
			return "模型 " + m + " 重复"
		}
		seen[m] = true
	}
	return ""
}

// compileRules compiles a stored (already validated) snapshot.
func compileRules(rules []Rule) ([]*compiledRule, error) {
	out := make([]*compiledRule, 0, len(rules))
	for _, r := range rules {
		r.normalize()
		c, err := compileRule(r, nil, "")
		if err != nil {
			return nil, fmt.Errorf("subscription: invalid stored rule %q", r.ID)
		}
		out = append(out, c)
	}
	return out, nil
}

// covers reports whether a model list (empty = all) includes model.
func covers(models []string, model string) bool {
	return len(models) == 0 || slices.Contains(models, model)
}

// appliesTo reports whether the rule meters model.
func (c *compiledRule) appliesTo(model string) bool { return covers(c.Models, model) }

// units is the weighted meter value of one request (§3).
func (c *compiledRule) units(model string, u protocol.Usage, charge money.Amount) Dec {
	var v Dec
	switch c.Meter {
	case MeterRequests:
		v = DecInt(1)
	case MeterTokensInput:
		v = DecInt(u.Input + u.CacheRead + u.CacheWrite)
	case MeterTokensOutput:
		v = DecInt(u.Output)
	case MeterTokensTotal:
		v = DecInt(u.Input + u.CacheRead + u.CacheWrite + u.Output)
	case MeterCharge:
		v = DecNano(int64(charge))
	case MeterImages:
		v = DecInt(u.Images)
	case MeterAudioSeconds:
		v = DecInt(u.AudioSeconds)
	}
	if w, ok := c.weights[model]; ok {
		v = v.Mul(w)
	}
	if v.Sign() < 0 {
		return Dec{}
	}
	return v
}

var locations sync.Map // name → *time.Location

// loadLocation is time.LoadLocation with a process-wide cache (zone files are
// read from disk on every LoadLocation call).
func loadLocation(name string) (*time.Location, error) {
	if name == "" {
		name = defaultTimezone
	}
	if l, ok := locations.Load(name); ok {
		return l.(*time.Location), nil
	}
	// "Local" depends on the host and is never a valid plan timezone.
	if name == "Local" {
		return nil, fmt.Errorf("invalid timezone")
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	locations.Store(name, l)
	return l, nil
}
