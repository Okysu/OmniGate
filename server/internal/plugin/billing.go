package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"omnigate/internal/platform/db"
	"omnigate/internal/plugin/engine"
)

// MeterTimeout bounds each computeUnits call (phase9-api.md §3).
const MeterTimeout = 5 * time.Millisecond

var metricBillingErrors = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "omnigate_billing_plugin_errors_total",
	Help: "Billing plugin meter evaluations that counted 0 units, by plugin, meter and reason (timeout, exception, invalid, unavailable).",
}, []string{"plugin", "meter", "reason"})

// BillingCtx is the ctx argument of computeUnits (phase9-api.md §3).
type BillingCtx struct {
	Model        string  `json:"model"`
	ServedModel  string  `json:"servedModel"`
	ChannelID    string  `json:"channelId"`
	ChannelTier  string  `json:"channelTier"`
	UserGroup    string  `json:"userGroup"`
	Inbound      string  `json:"inbound"`
	ImageCount   int64   `json:"imageCount"`
	AudioSeconds float64 `json:"audioSeconds"`
}

// MeterError explains why a meter evaluated to 0.
type MeterError struct {
	Reason  string // timeout | exception | invalid | unavailable
	Message string
}

func (e *MeterError) Error() string { return e.Reason + ": " + e.Message }

// SplitMeterRef splits "<pluginKey>.<meter>" (the part after "custom:").
func SplitMeterRef(ref string) (key, meter string, ok bool) {
	i := strings.LastIndex(ref, ".")
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	key, meter = ref[:i], ref[i+1:]
	return key, meter, pluginIDRe.MatchString(key) && meterNameRe.MatchString(meter)
}

// ResolveMeter returns the newest approved version of the enabled plugin key
// that declares meter (rule validation at plan save time); problem is a
// user-facing reason when there is none.
func (s *Service) ResolveMeter(ctx context.Context, key, meter string) (versionID uuid.UUID, decl MeterDecl, problem string, err error) {
	var pid uuid.UUID
	var status string
	err = s.pool.QueryRow(ctx, `SELECT id, status FROM plugins WHERE plugin_key = $1`, key).Scan(&pid, &status)
	if db.IsNoRows(err) {
		return uuid.Nil, decl, fmt.Sprintf("插件 %s 不存在", key), nil
	}
	if err != nil {
		return uuid.Nil, decl, "", err
	}
	if status != "enabled" {
		return uuid.Nil, decl, fmt.Sprintf("插件 %s 已停用", key), nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id, manifest FROM plugin_versions WHERE plugin_id = $1 AND approval = 'approved'`, pid)
	if err != nil {
		return uuid.Nil, decl, "", err
	}
	defer rows.Close()
	var best *Manifest
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return uuid.Nil, decl, "", err
		}
		var m Manifest
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if _, ok := m.Meter(meter); ok && (best == nil || CompareSemver(m.Version, best.Version) > 0) {
			mm := m
			best, versionID = &mm, id
		}
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, decl, "", err
	}
	if best == nil {
		return uuid.Nil, decl, fmt.Sprintf("插件 %s 没有已批准且声明了计量 %s 的版本", key, meter), nil
	}
	decl, _ = best.Meter(meter)
	return versionID, decl, "", nil
}

// MeterOption is a selectable custom meter (latest approved version of an
// enabled billing plugin).
type MeterOption struct {
	Meter      string    `json:"meter"` // custom:<pluginKey>.<name>
	Label      string    `json:"label"`
	Unit       string    `json:"unit,omitempty"`
	PluginID   uuid.UUID `json:"pluginId"`
	PluginKey  string    `json:"pluginKey"`
	PluginName string    `json:"pluginName"`
	Version    string    `json:"version"`
	VersionID  uuid.UUID `json:"versionId"`
}

// MeterOptions lists the custom meters of enabled plugins.
func (s *Service) MeterOptions(ctx context.Context) ([]MeterOption, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id, p.plugin_key, p.name, v.id, v.manifest FROM plugin_versions v
		JOIN plugins p ON p.id = v.plugin_id WHERE p.status = 'enabled' AND v.approval = 'approved'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type best struct {
		m    Manifest
		vid  uuid.UUID
		name string
		key  string
	}
	latest := map[uuid.UUID]*best{}
	for rows.Next() {
		var pid, vid uuid.UUID
		var key, name string
		var raw []byte
		if err := rows.Scan(&pid, &key, &name, &vid, &raw); err != nil {
			return nil, err
		}
		var m Manifest
		if json.Unmarshal(raw, &m) != nil || !m.HasKind(KindBilling) || m.Billing == nil {
			continue
		}
		if b := latest[pid]; b == nil || CompareSemver(m.Version, b.m.Version) > 0 {
			latest[pid] = &best{m: m, vid: vid, name: name, key: key}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []MeterOption{}
	for pid, b := range latest {
		for _, name := range sortedKeys(b.m.Billing.Meters) {
			d := b.m.Billing.Meters[name]
			out = append(out, MeterOption{Meter: "custom:" + b.key + "." + name, Label: d.Label, Unit: d.Unit, PluginID: pid,
				PluginKey: b.key, PluginName: b.name, Version: b.m.Version, VersionID: b.vid})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meter < out[j].Meter })
	return out, nil
}

// MeterUnits evaluates billing.meters[meter].computeUnits(usage, ctx) of a
// pinned plugin version and returns the units as a decimal string (≥ 0, at
// most 9 fractional digits). Every failure — the plugin or version is
// unavailable, the call times out (5 ms) or throws, or the result is not a
// non-negative number — yields "0", a warning log and
// omnigate_billing_plugin_errors_total; the error is returned for callers that
// want to show it (the editor's test runner).
func (s *Service) MeterUnits(ctx context.Context, versionID uuid.UUID, key, meter string, usage any, bc BillingCtx) (string, error) {
	units, err := s.meterUnits(ctx, versionID, key, meter, usage, bc)
	if err != nil {
		var me *MeterError
		if !errors.As(err, &me) {
			me = &MeterError{Reason: "unavailable", Message: err.Error()}
		}
		metricBillingErrors.WithLabelValues(key, meter, me.Reason).Inc()
		s.log.Warn("billing plugin meter counted as 0", "plugin", key, "meter", meter, "version_id", versionID,
			"reason", me.Reason, "error", me.Message)
		return "0", me
	}
	return units, nil
}

func (s *Service) meterUnits(ctx context.Context, versionID uuid.UUID, key, meter string, usage any, bc BillingCtx) (string, error) {
	unavailable := func(format string, a ...any) error {
		return &MeterError{Reason: "unavailable", Message: fmt.Sprintf(format, a...)}
	}
	l, err := s.Loaded(ctx, versionID)
	if err != nil {
		return "", unavailable("插件版本不可用：%v", err)
	}
	if l.Key != key {
		return "", unavailable("规则快照中的插件版本不属于 %s", key)
	}
	if l.Approval != "approved" {
		return "", unavailable("插件版本未批准")
	}
	if !s.Enabled(l.PluginID) {
		return "", unavailable("插件已停用")
	}
	if _, ok := l.Manifest.Meter(meter); !ok {
		return "", unavailable("插件版本 %s 没有声明计量 %s", l.Version, meter)
	}
	return l.computeUnits(ctx, meter, usage, bc, MeterTimeout)
}

// computeUnits runs one meter in a pure environment (no network, storage or
// secrets).
func (l *Loaded) computeUnits(ctx context.Context, meter string, usage any, bc BillingCtx, timeout time.Duration) (string, error) {
	if l.prog == nil {
		return "", &MeterError{Reason: "unavailable", Message: "插件没有可执行代码"}
	}
	r, err := l.prog.Call(ctx, &engine.Env{Pure: true}, []string{"billing", "meters", meter, "computeUnits"}, timeout, usage, bc)
	if err != nil {
		var pe *engine.Error
		if errors.As(err, &pe) {
			reason := "exception"
			if pe.Kind == "timeout" {
				reason = "timeout"
			}
			return "", &MeterError{Reason: reason, Message: pe.Message}
		}
		return "", &MeterError{Reason: "exception", Message: err.Error()}
	}
	units, msg := parseUnits(r.Output)
	if msg != "" {
		return "", &MeterError{Reason: "invalid", Message: msg}
	}
	return units, nil
}

// parseUnits accepts a finite non-negative number or decimal string and
// returns it as a plain decimal with at most 9 fractional digits (rounded
// half up); msg explains a rejected value.
func parseUnits(raw json.RawMessage) (string, string) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", "computeUnits 的返回值不是数字"
	}
	var r *big.Rat
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return "", "computeUnits 返回了非有限数"
		}
		r = new(big.Rat)
		r.SetString(strconv.FormatFloat(x, 'f', -1, 64))
	case string:
		var ok bool
		s := strings.TrimSpace(x)
		if s == "" || strings.ContainsAny(s, "eE/") {
			return "", "computeUnits 返回的字符串不是十进制数"
		}
		if r, ok = new(big.Rat).SetString(s); !ok {
			return "", "computeUnits 返回的字符串不是十进制数"
		}
	default:
		return "", "computeUnits 必须返回 number 或十进制字符串"
	}
	if r.Sign() < 0 {
		return "", "computeUnits 返回了负数"
	}
	out := r.FloatString(9)
	if strings.Contains(out, ".") {
		out = strings.TrimRight(strings.TrimRight(out, "0"), ".")
	}
	if len(out) > 38 {
		return "", "computeUnits 返回的数值过大"
	}
	return out, ""
}
