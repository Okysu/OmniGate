package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/netguard"
	"omnigate/internal/plugin"
	"omnigate/internal/protocol"
)

// Service implements channel use cases with authorization.
type Service struct {
	store     *Store
	reg       *Registry
	audit     *audit.Recorder
	plugins   *plugin.Service
	userAgent string

	// OnBalance, when set, receives every fresh upstream balance reading
	// (capability results whose output kind is "balance").
	OnBalance func(ctx context.Context, r BalanceReading)
	// OnShareInvited, when set, receives committed share invitations
	// (phase5-api.md §5.5); it must not block.
	OnShareInvited func(ctx context.Context, inv ShareInvite)
}

func NewService(store *Store, reg *Registry, rec *audit.Recorder, plugins *plugin.Service, userAgent string) *Service {
	return &Service{store: store, reg: reg, audit: rec, plugins: plugins, userAgent: userAgent}
}

// Meta carries request context for audit entries.
type Meta struct {
	IPPrefix  string
	RequestID string
}

func canManage(p *authz.Principal, c *Channel) bool {
	return c.Owner.ID == p.UserID || p.Can(authz.ChannelsManage)
}

func (s *Service) canSee(ctx context.Context, p *authz.Principal, c *Channel) (bool, error) {
	if canManage(p, c) {
		return true, nil
	}
	var group *uuid.UUID
	if c.Scope == authz.ScopeShared && len(c.SharedWith.Groups) > 0 {
		var err error
		if group, err = s.store.userGroup(ctx, p.UserID); err != nil {
			return false, err
		}
	}
	return p.CanOnResource(authz.ActionUse, c.Resource(group), authz.ChannelsManage), nil
}

// View is either a full Channel or a Summary depending on the caller.
type View any

func (s *Service) view(p *authz.Principal, c *Channel) View {
	c.Health = s.reg.Breaker.Health(c.ID)
	if canManage(p, c) {
		return c
	}
	return c.Summary()
}

func (s *Service) List(ctx context.Context, p *authz.Principal, q ListQuery) ([]View, int, error) {
	q.Viewer, q.All = p.UserID, p.Can(authz.ChannelsManage)
	list, total, err := s.store.List(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	var managed []*Channel
	for _, c := range list {
		if canManage(p, c) {
			managed = append(managed, c)
		}
	}
	s.attachBadges(ctx, managed)
	out := make([]View, len(list))
	for i, c := range list {
		out[i] = s.view(p, c)
	}
	return out, total, nil
}

func (s *Service) Get(ctx context.Context, p *authz.Principal, id uuid.UUID) (View, error) {
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if ok, err := s.canSee(ctx, p, c); err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, apperr.NotFound("渠道")
	}
	return s.view(p, c), nil
}

// loadForManage fetches a channel the caller must be able to manage.
func (s *Service) loadForManage(ctx context.Context, p *authz.Principal, id uuid.UUID) (*Channel, error) {
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if ok, err := s.canSee(ctx, p, c); err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, apperr.NotFound("渠道")
	}
	if !canManage(p, c) {
		return nil, apperr.Forbidden()
	}
	return c, nil
}

func (s *Service) checkScope(p *authz.Principal, c *Channel) error {
	if c.Scope == authz.ScopeGlobal && !p.Can(authz.ChannelsManage) {
		return apperr.New(apperr.KindForbidden, "forbidden", "只有渠道管理员可以发布全局渠道")
	}
	return nil
}

// checkBaseURL rejects obviously internal targets early for owners who may not
// use the private network. The dial-time check in netguard stays authoritative.
func (s *Service) checkBaseURL(ownerRole identity.Role, raw string) error {
	if s.reg.opts.AllowPrivateNetwork && (&authz.Principal{Role: ownerRole}).Can(authz.ChannelsManage) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return baseURLNotAllowed()
	}
	if ip, err := netip.ParseAddr(host); err == nil && !netguard.IsPublic(ip) {
		return baseURLNotAllowed()
	}
	return nil
}

func baseURLNotAllowed() error {
	return apperr.New(apperr.KindValidation, "base_url_not_allowed",
		"渠道地址指向内网或本机地址，出于安全原因不允许（管理员可开启 OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK）")
}

func (s *Service) Create(ctx context.Context, p *authz.Principal, in Input, meta Meta) (*Channel, error) {
	if !p.Can(authz.ChannelsWrite) {
		return nil, apperr.Forbidden()
	}
	c := &Channel{Scope: authz.ScopePrivate, Status: "enabled", Weight: 1, Owner: OwnerRef{ID: p.UserID, DisplayName: p.Name},
		PluginConfig: map[string]any{}, SecretFields: map[string]SecretInfo{}, Shares: []ChannelShare{}}
	in.groupsAllowed = p.Can(authz.ChannelsManage)
	l, err := s.preparePlugin(ctx, c, &in, true)
	if err != nil {
		return nil, err
	}
	if err := in.apply(c, true); err != nil {
		return nil, err
	}
	secrets, _, err := s.applyPlugin(ctx, c, in, l, true)
	if err != nil {
		return nil, err
	}
	if err := s.checkScope(p, c); err != nil {
		return nil, err
	}
	if err := s.checkBaseURL(p.Role, c.BaseURL); err != nil {
		return nil, err
	}
	md := map[string]any{"name": c.Name, "type": c.Type, "scope": c.Scope}
	if c.Plugin != nil {
		md["plugin"] = c.Plugin.Key + "@" + c.Plugin.Version
	}
	err = s.store.Create(ctx, c, *in.APIKey, secrets, func(tx db.Tx) error {
		if len(c.invited) > 0 {
			md["invited"] = c.invited
		}
		return s.record(ctx, tx, p, meta, "channel.create", c, md)
	})
	if err != nil {
		return nil, err
	}
	s.reg.Invalidate()
	s.invited(ctx, c)
	c.Health = s.reg.Breaker.Health(c.ID)
	return c, nil
}

func (s *Service) Update(ctx context.Context, p *authz.Principal, id uuid.UUID, in Input, meta Meta) (*Channel, error) {
	c, err := s.loadForManage(ctx, p, id)
	if err != nil {
		return nil, err
	}
	before := *c
	in.groupsAllowed = p.Can(authz.ChannelsManage)
	l, err := s.preparePlugin(ctx, c, &in, false)
	if err != nil {
		return nil, err
	}
	if err := in.apply(c, false); err != nil {
		return nil, err
	}
	secrets, change, err := s.applyPlugin(ctx, c, in, l, false)
	if err != nil {
		return nil, err
	}
	if c.Scope != before.Scope || c.Scope == authz.ScopeGlobal {
		if err := s.checkScope(p, c); err != nil {
			return nil, err
		}
	}
	if c.BaseURL != before.BaseURL {
		role, err := s.store.ownerRole(ctx, c.Owner.ID)
		if err != nil {
			return nil, err
		}
		if err := s.checkBaseURL(role, c.BaseURL); err != nil {
			return nil, err
		}
	}
	changed := diffFields(&before, c)
	if in.APIKey != nil {
		changed = append(changed, "apiKey")
	}
	if in.Secrets != nil {
		changed = append(changed, "secrets")
	}
	if in.PluginConfig != nil {
		changed = append(changed, "pluginConfig")
	}
	err = s.store.Update(ctx, c, *in.Version, in.APIKey, secrets, func(tx db.Tx) error {
		if change != nil {
			if err := s.record(ctx, tx, p, meta, "channel.plugin_upgrade", c, map[string]any{"name": c.Name, "plugin": c.Plugin.Key,
				"from": change.From, "to": change.To}); err != nil {
				return err
			}
		}
		md := map[string]any{"name": c.Name, "changed": changed}
		if len(c.invited) > 0 {
			md["invited"] = c.invited
		}
		return s.record(ctx, tx, p, meta, "channel.update", c, md)
	})
	if err != nil {
		return nil, err
	}
	s.reg.Invalidate()
	s.invited(ctx, c)
	c.Health = s.reg.Breaker.Health(c.ID)
	return c, nil
}

func (s *Service) Delete(ctx context.Context, p *authz.Principal, id uuid.UUID, meta Meta) error {
	c, err := s.loadForManage(ctx, p, id)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, id, func(tx db.Tx) error {
		return s.record(ctx, tx, p, meta, "channel.delete", c, map[string]any{"name": c.Name})
	}); err != nil {
		return err
	}
	s.reg.Invalidate()
	return nil
}

func (s *Service) record(ctx context.Context, tx db.Tx, p *authz.Principal, meta Meta, action string, c *Channel, md map[string]any) error {
	id := c.ID.String()
	return s.audit.Record(ctx, tx, audit.Entry{
		ActorID: &p.UserID, ActorName: &p.Name, Action: action, ResourceType: "channel", ResourceID: &id,
		IPPrefix: meta.IPPrefix, RequestID: meta.RequestID, Metadata: md,
	})
}

func diffFields(a, b *Channel) []string {
	var out []string
	add := func(cond bool, f string) {
		if cond {
			out = append(out, f)
		}
	}
	add(a.Name != b.Name, "name")
	add(a.BaseURL != b.BaseURL, "baseUrl")
	add(a.Scope != b.Scope, "scope")
	add(a.Status != b.Status, "status")
	add(a.Priority != b.Priority, "priority")
	add(a.Weight != b.Weight, "weight")
	am, _ := json.Marshal(a.Models)
	bm, _ := json.Marshal(b.Models)
	add(string(am) != string(bm), "models")
	ac, _ := json.Marshal(a.Config)
	bc, _ := json.Marshal(b.Config)
	add(string(ac) != string(bc), "config")
	as, _ := json.Marshal(a.SharedWith)
	bs, _ := json.Marshal(b.SharedWith)
	add(string(as) != string(bs), "sharedWith")
	add(fmt.Sprint(a.PluginVersionID) != fmt.Sprint(b.PluginVersionID) && a.PluginVersionID != nil, "pluginVersionId")
	aa, ba := a.Alerts.BalanceBelow, b.Alerts.BalanceBelow
	add((aa == nil) != (ba == nil) || aa != nil && *aa != *ba, "alerts")
	return out
}

// runtimeFor builds a Runtime for test/discover (works for disabled channels too).
func (s *Service) runtimeFor(ctx context.Context, c *Channel) (*Runtime, error) {
	key, err := s.store.apiKey(ctx, c.ID)
	if err != nil {
		return nil, apperr.New(apperr.KindConflict, "secret_unavailable", "渠道密钥无法读取（可能主密钥已更换），请重新填写 API Key")
	}
	role, err := s.store.ownerRole(ctx, c.Owner.ID)
	if err != nil {
		return nil, err
	}
	rt := &Runtime{Channel: *c, APIKey: key}
	rt.AllowPrivate = s.reg.opts.AllowPrivateNetwork && (&authz.Principal{Role: role}).Can(authz.ChannelsManage)
	return rt, nil
}

// TestResult is returned by Test.
type TestResult struct {
	OK         bool    `json:"ok"`
	LatencyMs  int64   `json:"latencyMs"`
	StatusCode int     `json:"statusCode"`
	Error      *string `json:"error"`
}

// probe performs the cheap model-list request.
func (s *Service) probe(ctx context.Context, rt *Runtime) (int, []byte, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := rt.NewRequest(ctx, "models", nil, nil, s.userAgent)
	if err != nil {
		return 0, nil, 0, err
	}
	start := time.Now()
	resp, err := s.reg.Do(rt, req)
	if err != nil {
		return 0, nil, time.Since(start), err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, body, time.Since(start), err
}

// describeNetErr turns transport errors into short user-facing reasons
// without leaking Go error internals.
func describeNetErr(err error) string {
	var ne net.Error
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, netguard.ErrBlocked):
		return "目标地址被安全策略拒绝（内网/本机地址）"
	case errors.As(err, &dnsErr):
		return "无法解析上游域名"
	case errors.As(err, &ne) && ne.Timeout():
		return "连接上游超时"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "上游拒绝连接（端口未开放）"
	case strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "x509:"):
		return "与上游的 TLS 握手失败（证书或协议问题）"
	case errors.As(err, &opErr):
		return "无法连接到上游"
	default:
		return "请求上游失败"
	}
}

func (s *Service) Test(ctx context.Context, p *authz.Principal, id uuid.UUID) (*TestResult, error) {
	c, err := s.loadForManage(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if c.Type == TypeCustom {
		return s.testCustom(ctx, c)
	}
	rt, err := s.runtimeFor(ctx, c)
	if err != nil {
		return nil, err
	}
	status, body, lat, err := s.probe(ctx, rt)
	res := &TestResult{LatencyMs: lat.Milliseconds(), StatusCode: status}
	switch {
	case err != nil:
		msg := describeNetErr(err)
		res.Error = &msg
		s.reg.Breaker.Failure(c.ID, msg)
	case status != 200:
		msg := fmt.Sprintf("上游返回 %d：%s", status, upstreamMsg(body))
		res.Error = &msg
		if status == 401 || status == 403 {
			s.reg.ReportAuthFailure(c.ID, status)
		}
		s.reg.Breaker.Failure(c.ID, msg)
	default:
		res.OK = true
		s.reg.Breaker.Success(c.ID)
	}
	return res, nil
}

func (s *Service) DiscoverModels(ctx context.Context, p *authz.Principal, id uuid.UUID) ([]string, error) {
	c, err := s.loadForManage(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if c.Type == TypeCustom {
		return s.discoverCustom(ctx, c)
	}
	rt, err := s.runtimeFor(ctx, c)
	if err != nil {
		return nil, err
	}
	status, body, _, err := s.probe(ctx, rt)
	if err != nil {
		return nil, apperr.New(apperr.KindUpstream, "upstream_error", describeNetErr(err))
	}
	if status != 200 {
		return nil, apperr.New(apperr.KindUpstream, "upstream_error", fmt.Sprintf("上游返回 %d：%s", status, upstreamMsg(body)))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, apperr.New(apperr.KindUpstream, "upstream_error", "上游模型列表格式无法识别")
	}
	out := []string{}
	for _, m := range list.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func upstreamMsg(body []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		msg = env.Error.Message
	}
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return protocol.Redact(msg)
}

// testCustom tests a custom-protocol channel through its plugin's
// health.check (or models.list) capability.
func (s *Service) testCustom(ctx context.Context, c *Channel) (*TestResult, error) {
	res, _, err := s.customCapability(ctx, c, "health.check", "models.list")
	if err != nil {
		return nil, err
	}
	out := &TestResult{LatencyMs: res.DurationMs, StatusCode: http.StatusOK}
	var health struct {
		OK      *bool  `json:"ok"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(res.Output, &health)
	switch {
	case res.Error != nil:
		msg := "插件能力执行失败：" + *res.Error
		out.Error, out.StatusCode = &msg, 0
		s.reg.Breaker.Failure(c.ID, msg)
	case health.OK != nil && !*health.OK:
		msg := "健康检查未通过"
		if health.Message != "" {
			msg += "：" + health.Message
		}
		out.Error = &msg
		s.reg.Breaker.Failure(c.ID, msg)
	default:
		out.OK = true
		s.reg.Breaker.Success(c.ID)
	}
	return out, nil
}

// discoverCustom lists upstream models through the plugin's models.list.
func (s *Service) discoverCustom(ctx context.Context, c *Channel) ([]string, error) {
	res, _, err := s.customCapability(ctx, c, "models.list")
	if err != nil {
		return nil, err
	}
	if res.Error != nil {
		return nil, apperr.New(apperr.KindUpstream, "upstream_error", "插件能力执行失败："+*res.Error)
	}
	var list struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	_ = json.Unmarshal(res.Output, &list)
	out := []string{}
	for _, m := range list.Models {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}
