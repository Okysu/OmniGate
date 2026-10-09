package channel

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/plugin"
)

// CapabilityInfo describes a declared capability for the UI.
type CapabilityInfo struct {
	Name            string `json:"name"`
	Label           string `json:"label"`
	Output          string `json:"output"`
	UserTriggerable bool   `json:"userTriggerable"`
	Schedule        string `json:"schedule"`
}

type capabilitiesView struct {
	Plugin       *PluginRef                          `json:"plugin"`
	Capabilities []CapabilityInfo                    `json:"capabilities"`
	Results      map[string]*plugin.CapabilityResult `json:"results"`
	UI           []plugin.UIContribution             `json:"ui"`
}

// runtimeForPlugin returns the channel runtime (registry snapshot when the
// channel is enabled, otherwise built on demand) and its plugin.
func (s *Service) runtimeForPlugin(ctx context.Context, c *Channel) (*Runtime, *plugin.Loaded, error) {
	if c.PluginVersionID == nil || s.plugins == nil {
		return nil, nil, nil
	}
	l, err := s.plugins.Loaded(ctx, *c.PluginVersionID)
	if err != nil || l.Builtin {
		return nil, nil, err
	}
	if rt, ok := s.reg.Get(c.ID); ok && rt.Plugin != nil && rt.Plugin.VersionID == l.VersionID {
		return rt, l, nil
	}
	rt, err := s.runtimeFor(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	if rt.PluginSecrets, err = s.store.pluginSecrets(ctx, c.ID); err != nil {
		return nil, nil, err
	}
	rt.Plugin = l
	return rt, l, nil
}

func (s *Service) Capabilities(ctx context.Context, p *authz.Principal, id uuid.UUID) (*capabilitiesView, error) {
	c, err := s.loadForManage(ctx, p, id)
	if err != nil {
		return nil, err
	}
	v := &capabilitiesView{Plugin: c.Plugin, Capabilities: []CapabilityInfo{}, Results: map[string]*plugin.CapabilityResult{}, UI: []plugin.UIContribution{}}
	_, l, err := s.runtimeForPlugin(ctx, c)
	if err != nil || l == nil {
		return v, err
	}
	for name, decl := range l.Manifest.Capabilities {
		info := CapabilityInfo{Name: name, Label: decl.Label, Output: decl.Output, UserTriggerable: decl.UserTriggerable}
		if decl.Schedule != nil && slices.Contains(l.Manifest.Permissions.Schedule, name) {
			info.Schedule = decl.Schedule.MinInterval
		}
		if info.Label == "" {
			info.Label = name
		}
		v.Capabilities = append(v.Capabilities, info)
	}
	sort.Slice(v.Capabilities, func(i, j int) bool { return v.Capabilities[i].Name < v.Capabilities[j].Name })
	if v.Results, err = s.plugins.Results(ctx, c.ID); err != nil {
		return nil, err
	}
	v.UI = l.Manifest.Contributions()
	return v, nil
}

func (s *Service) InvokeCapability(ctx context.Context, p *authz.Principal, id uuid.UUID, name string, input json.RawMessage, meta Meta) (*plugin.CapabilityResult, error) {
	c, err := s.loadForManage(ctx, p, id)
	if err != nil {
		return nil, err
	}
	rt, l, err := s.runtimeForPlugin(ctx, c)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, apperr.New(apperr.KindNotFound, "capability_not_found", "该渠道没有插件能力")
	}
	decl, ok := l.Manifest.Capabilities[name]
	if !ok {
		return nil, apperr.New(apperr.KindNotFound, "capability_not_found", "插件没有声明能力 "+name)
	}
	if !decl.UserTriggerable {
		return nil, apperr.New(apperr.KindForbidden, "capability_forbidden", "该能力不允许手动触发")
	}
	if !s.plugins.Enabled(l.PluginID) {
		return nil, apperr.New(apperr.KindConflict, "plugin_disabled", "该插件已停用")
	}
	var in any
	if len(input) > 0 && string(input) != "null" {
		in = input
	}
	res := l.CallCapability(ctx, rt.PluginEnv(s.reg.HTTPFor(rt), s.userAgent), name, in, nil)
	if err := s.plugins.SaveResult(ctx, c.ID, l.VersionID, name, res); err != nil {
		return nil, err
	}
	s.afterResult(ctx, c.ID, l, name, res)
	rid := c.ID.String()
	_ = s.audit.Record(ctx, nil, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: "capability.invoke", ResourceType: "channel",
		ResourceID: &rid, IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
		Metadata: map[string]any{"capability": name, "ok": res.OK, "plugin": l.Key + "@" + l.Version}})
	return res, nil
}

// attachBadges resolves channel.list.badge contributions for managed channels.
func (s *Service) attachBadges(ctx context.Context, list []*Channel) {
	if s.plugins == nil {
		return
	}
	for _, c := range list {
		if c.PluginVersionID == nil {
			continue
		}
		l, err := s.plugins.Loaded(ctx, *c.PluginVersionID)
		if err != nil || l.Builtin {
			continue
		}
		var badges []plugin.UIContribution
		for _, ui := range l.Manifest.Contributions() {
			if ui.Slot == "channel.list.badge" {
				badges = append(badges, ui)
			}
		}
		if len(badges) == 0 {
			continue
		}
		results, err := s.plugins.Results(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, b := range badges {
			v, ok := resolveBind(b.Component.Bind, results)
			if !ok {
				continue
			}
			badge := Badge{Value: v, Format: b.Component.Format}
			if cur, ok := resolveBind(b.Component.CurrencyBind, results); ok {
				badge.Currency, _ = cur.(string)
			}
			c.Badges = append(c.Badges, badge)
		}
	}
}

// resolveBind evaluates "<capability>:<JSON Pointer>" against latest results.
func resolveBind(bind string, results map[string]*plugin.CapabilityResult) (any, bool) {
	name, ptr, ok := strings.Cut(bind, ":")
	if !ok {
		return nil, false
	}
	r, ok := results[name]
	if !ok || !r.OK || r.Unsupported || len(r.Output) == 0 {
		return nil, false
	}
	var cur any
	if json.Unmarshal(r.Output, &cur) != nil {
		return nil, false
	}
	for _, seg := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		if seg == "" {
			continue
		}
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		switch x := cur.(type) {
		case map[string]any:
			cur, ok = x[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur, ok = x[i], true
		default:
			return nil, false
		}
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// RunCapabilityScheduler executes schedulable capabilities when their last
// result is older than minInterval (single instance; bounded concurrency).
func (s *Service) RunCapabilityScheduler(ctx context.Context, log *slog.Logger) {
	if s.plugins == nil {
		return
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, rt := range s.reg.All() {
			l := rt.Plugin
			if l == nil || !s.plugins.Enabled(l.PluginID) {
				continue
			}
			results, err := s.plugins.Results(ctx, rt.ID)
			if err != nil {
				log.Warn("capability scheduler: results", "err", err)
				continue
			}
			for name, decl := range l.Manifest.Capabilities {
				iv := decl.MinInterval()
				if iv <= 0 || !slices.Contains(l.Manifest.Permissions.Schedule, name) {
					continue
				}
				if r, ok := results[name]; ok && time.Since(r.FetchedAt) < iv {
					continue
				}
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				wg.Add(1)
				go func(rt *Runtime, l *plugin.Loaded, name string) {
					defer func() { <-sem; wg.Done() }()
					res := l.CallCapability(ctx, rt.PluginEnv(s.reg.HTTPFor(rt), s.userAgent), name, nil, nil)
					if err := s.plugins.SaveResult(context.WithoutCancel(ctx), rt.ID, l.VersionID, name, res); err != nil {
						log.Warn("capability scheduler: save", "channel_id", rt.ID, "err", err)
						return
					}
					s.afterResult(context.WithoutCancel(ctx), rt.ID, l, name, res)
				}(rt, l, name)
			}
		}
	}
}

// ---- HTTP ----

func (h *Handler) capabilityRoutes(r chi.Router) {
	r.Get("/{id}/capabilities", h.listCapabilities)
	r.Post("/{id}/capabilities/{name}", h.invokeCapability)
}

func (h *Handler) listCapabilities(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.Capabilities(r.Context(), auth.PrincipalFrom(r.Context()), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) invokeCapability(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Input json.RawMessage `json:"input"`
	}
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &body); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	res, err := h.svc.InvokeCapability(r.Context(), auth.PrincipalFrom(r.Context()), id, chi.URLParam(r, "name"), body.Input, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// customCapability runs a capability of a custom-protocol channel's plugin for
// the connectivity test (health.check, else models.list) and model discovery
// (models.list): such upstreams have no standard model list endpoint.
func (s *Service) customCapability(ctx context.Context, c *Channel, names ...string) (*plugin.CapabilityResult, string, error) {
	rt, l, err := s.runtimeForPlugin(ctx, c)
	if err != nil {
		return nil, "", err
	}
	if l == nil {
		return nil, "", apperr.New(apperr.KindConflict, "plugin_unavailable", "自定义协议渠道的插件版本不可用")
	}
	for _, name := range names {
		if _, ok := l.Manifest.Capabilities[name]; ok {
			return l.CallCapability(ctx, rt.PluginEnv(s.reg.HTTPFor(rt), s.userAgent), name, nil, nil), name, nil
		}
	}
	return nil, "", apperr.New(apperr.KindConflict, "capability_not_found",
		"自定义协议渠道需要插件实现 "+strings.Join(names, " 或 ")+" 能力才能执行此操作")
}
