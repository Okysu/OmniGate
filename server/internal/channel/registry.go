package channel

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/platform/netguard"
	"omnigate/internal/plugin"
	"omnigate/internal/plugin/engine"
	"omnigate/internal/protocol"
)

// Runtime is an enabled channel prepared for the data plane.
type Runtime struct {
	Channel
	APIKey        string
	PluginSecrets map[string]string
	AllowPrivate  bool
	// Plugin is the pinned plugin version (nil when none or built-in only).
	Plugin   *plugin.Loaded
	shared   map[uuid.UUID]bool
	upstream map[string]string
	// platform is set when the owner is a platform administrator
	// (system_admin or channel_admin) at the time of the snapshot.
	platform bool
}

// Tier is a channel's relation to the requesting user (docs/contracts/phase5-api.md §1).
type Tier string

const (
	TierOwn      Tier = "own"      // owned by the user: never billed
	TierShared   Tier = "shared"   // another regular user's channel shared with (and accepted by) the user: never billed
	TierPlatform Tier = "platform" // owned by a platform administrator: billed at sell prices
)

// Tiers lists the tiers in routing order.
var Tiers = []Tier{TierOwn, TierShared, TierPlatform}

// PlatformOwned reports whether the channel's owner is a platform administrator.
func (r *Runtime) PlatformOwned() bool { return r.platform }

// TierFor returns the channel's tier relative to userID: own when userID owns
// it, platform when an administrator owns it, shared otherwise (another
// regular user's channel, usable through a share).
func (r *Runtime) TierFor(userID uuid.UUID) Tier {
	switch {
	case r.Owner.ID == userID:
		return TierOwn
	case r.platform:
		return TierPlatform
	}
	return TierShared
}

// IsPlatformRole reports whether channels owned by role belong to the platform tier.
func IsPlatformRole(role identity.Role) bool {
	return role == identity.RoleSystemAdmin || role == identity.RoleChannelAdmin
}

// PluginEnv describes the channel to its plugin.
func (r *Runtime) PluginEnv(httpDoer engine.HTTPDoer, userAgent string) plugin.ChannelEnv {
	secrets := map[string]string{"apiKey": r.APIKey}
	for k, v := range r.PluginSecrets {
		secrets[k] = v
	}
	models := make([]plugin.ModelDefault, len(r.Models))
	for i, m := range r.Models {
		models[i] = plugin.ModelDefault{Model: m.Model, UpstreamModel: m.UpstreamModel}
	}
	return plugin.ChannelEnv{ID: r.ID, Name: r.Name, BaseURL: r.BaseURL, Models: models, Config: r.PluginConfig,
		Secrets: secrets, AllowPrivate: r.AllowPrivate, HTTP: httpDoer, UserAgent: userAgent}
}

// DefaultPath is the upstream path for a dialect relative to the base URL.
func (r *Runtime) DefaultPath(dialect string) string {
	switch dialect {
	case protocol.OpenAIChat:
		return "/chat/completions"
	case protocol.OpenAIResponses:
		return "/responses"
	case protocol.Anthropic:
		return "/v1/messages"
	case protocol.OpenAIEmbeddings:
		return "/embeddings"
	case protocol.OpenAICompletions:
		return "/completions"
	case protocol.OpenAIImagesGenerations:
		return "/images/generations"
	case protocol.OpenAIImagesEdits:
		return "/images/edits"
	case protocol.OpenAIImagesVariations:
		return "/images/variations"
	case protocol.OpenAIAudioTranscriptions:
		return "/audio/transcriptions"
	case protocol.OpenAIAudioTranslations:
		return "/audio/translations"
	case protocol.OpenAIAudioSpeech:
		return "/audio/speech"
	case "models":
		if r.Type == TypeAnthropic {
			return "/v1/models"
		}
		return "/models"
	}
	return "/"
}

// RequestOverride lets plugin hooks replace path/headers and take over auth,
// and carries the client headers a session affinity rule passes through.
type RequestOverride struct {
	Path     string
	Headers  map[string]string
	SkipAuth bool
	// Pass copies client headers to the upstream request (nil = none).
	Pass *PassHeaders
	// Fill sets headers the request does not carry after everything else
	// (channel config, plugin hooks, passed client headers): session
	// affinity's inject_session_header. Invalid and forbidden names are
	// ignored.
	Fill map[string]string
}

// PassHeaders are client headers a session affinity rule copies to the
// upstream request (pass_headers, docs/contracts/phase12-api.md §3).
type PassHeaders struct {
	Names []string
	// KeepOrigin keeps the value of a header the channel configuration sets
	// explicitly (config.headers, plugin hook headers); otherwise the client
	// value replaces it.
	KeepOrigin bool
}

// Apply copies the listed client headers into h. Forbidden headers
// (credentials, cookies, framing) are never copied; with KeepOrigin a header
// named in one of explicit (case-insensitive) keeps its value. Defaults the
// gateway sets on its own (User-Agent, anthropic-version) are not explicit
// configuration and are replaced. Header names are written in Go's canonical
// form ("Session_id", "X-Codex-Turn-Metadata"): HTTP header names are
// case-insensitive and underscores are kept.
func (p *PassHeaders) Apply(h, client http.Header, explicit ...map[string]string) {
	if p == nil || client == nil {
		return
	}
	for _, name := range p.Names {
		if !validHeaderName(name) || forbiddenHeaders[strings.ToLower(name)] {
			continue
		}
		vals := client.Values(name)
		if len(vals) == 0 {
			continue
		}
		if p.KeepOrigin && setExplicitly(name, explicit) {
			continue
		}
		h[http.CanonicalHeaderKey(name)] = slices.Clone(vals)
	}
}

func setExplicitly(name string, explicit []map[string]string) bool {
	for _, m := range explicit {
		for k := range m {
			if strings.EqualFold(k, name) {
				return true
			}
		}
	}
	return false
}

// SharedUsers returns the users a shared channel is shared with: users who
// accepted their share and the members of listed groups at snapshot time
// (unordered; none while the owner is disabled).
func (r *Runtime) SharedUsers() []uuid.UUID {
	if r.Scope != authz.ScopeShared {
		return nil
	}
	out := make([]uuid.UUID, 0, len(r.shared))
	for id := range r.shared {
		out = append(out, id)
	}
	return out
}

// UsableBy reports whether userID may route requests through this channel.
// Using a channel requires ownership, global scope or an effective share: a
// user share the user accepted, or a share with the user's group (set by a
// channels.manage administrator), while the owner is not disabled
// (docs/contracts/phase5-api.md §5.4);
// management permissions do not grant use of other users' private channels.
func (r *Runtime) UsableBy(userID uuid.UUID) bool {
	switch {
	case r.Owner.ID == userID, r.Scope == authz.ScopeGlobal:
		return true
	case r.Scope == authz.ScopeShared:
		return r.shared[userID]
	}
	return false
}

func (r *Runtime) UpstreamModel(model string) (string, bool) {
	m, ok := r.upstream[model]
	return m, ok
}

// Timeout is the response-header timeout for this channel.
func (r *Runtime) Timeout() time.Duration {
	if r.Config.TimeoutSeconds > 0 {
		return time.Duration(r.Config.TimeoutSeconds) * time.Second
	}
	return 60 * time.Second
}

// MaxTokensField for converted requests to openai channels.
func (r *Runtime) MaxTokensField() string {
	if r.Config.MaxTokensField != "" {
		return r.Config.MaxTokensField
	}
	return "max_tokens"
}

// passthroughAnthropicHeaders are client headers forwarded to Anthropic upstreams.
var passthroughAnthropicHeaders = []string{"anthropic-version", "anthropic-beta"}

// NewRequest builds an upstream request for dialect (a protocol.* name, or
// "models" for the model list) with a JSON body (nil = none). clientHeader and
// ov may be nil.
func (r *Runtime) NewRequest(ctx context.Context, dialect string, body []byte, clientHeader http.Header, userAgent string, ov ...*RequestOverride) (*http.Request, error) {
	if body == nil {
		return r.NewRequestBody(ctx, dialect, nil, "", 0, clientHeader, userAgent, ov...)
	}
	req, err := r.NewRequestBody(ctx, dialect, bytes.NewReader(body), "application/json", int64(len(body)), clientHeader, userAgent, ov...)
	if err != nil {
		return nil, err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return req, nil
}

// NewRequestBody is NewRequest with an arbitrary body (e.g. a streamed
// multipart encoding): contentType and length (-1 = unknown, chunked) describe
// it. A nil body sends no body (GET for "models").
func (r *Runtime) NewRequestBody(ctx context.Context, dialect string, body io.Reader, contentType string, length int64, clientHeader http.Header, userAgent string, ov ...*RequestOverride) (*http.Request, error) {
	method := http.MethodPost
	if dialect == "models" {
		method = http.MethodGet
	}
	path := r.DefaultPath(dialect)
	var o *RequestOverride
	if len(ov) > 0 && ov[0] != nil {
		o = ov[0]
		if o.Path != "" {
			path = o.Path
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, r.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if body != nil {
		rc, ok := body.(io.ReadCloser)
		if !ok {
			rc = io.NopCloser(body)
		}
		req.Body, req.ContentLength = rc, length
		if length == 0 {
			req.Body = http.NoBody
		}
	}
	for k, v := range r.Config.Headers {
		req.Header.Set(k, v)
	}
	if o != nil {
		for k, v := range o.Headers {
			if validHeaderName(k) && !forbiddenHeaders[strings.ToLower(k)] || (o.SkipAuth && (strings.EqualFold(k, "authorization") || strings.EqualFold(k, "x-api-key"))) {
				req.Header.Set(k, v)
			}
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", userAgent)
	if r.Type == TypeAnthropic {
		if o == nil || !o.SkipAuth {
			req.Header.Set("x-api-key", r.APIKey)
		}
		if clientHeader != nil {
			for _, h := range passthroughAnthropicHeaders {
				if v := clientHeader.Get(h); v != "" {
					req.Header.Set(h, v)
				}
			}
		}
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	} else if o == nil || !o.SkipAuth {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}
	if o != nil && o.Pass != nil {
		o.Pass.Apply(req.Header, clientHeader, r.Config.Headers, o.Headers)
	}
	if o != nil {
		for k, v := range o.Fill {
			if validHeaderName(k) && !forbiddenHeaders[strings.ToLower(k)] && len(req.Header.Values(k)) == 0 {
				req.Header.Set(k, v)
			}
		}
	}
	return req, nil
}

// RegistryOptions configure network policy.
type RegistryOptions struct {
	// Plugins resolves pinned plugin versions (nil disables plugins).
	Plugins *plugin.Service
	// AllowPrivateNetwork lets channels owned by users holding channels.manage
	// reach private/loopback addresses (local Ollama, intranet gateways).
	AllowPrivateNetwork bool
	Proxy               *url.URL
	RefreshInterval     time.Duration
	// Transport replaces the network-policy transports for every channel
	// (tests only: lets regular users' channels reach local fake upstreams).
	Transport http.RoundTripper
}

// Registry is the data plane's view of channels: an immutable snapshot swapped
// atomically on reload, plus health and HTTP clients.
type Registry struct {
	store   *Store
	log     *slog.Logger
	opts    RegistryOptions
	Breaker *Breaker

	guarded *http.Client
	open    *http.Client

	mu     sync.RWMutex
	all    []*Runtime
	byID   map[uuid.UUID]*Runtime
	reload chan struct{}

	// OnReload is called after every successful snapshot reload (must not block).
	OnReload func()
	// OnAuthFailure is called when an upstream rejects a channel's
	// credentials (HTTP 401/403); it must not block.
	OnAuthFailure func(id uuid.UUID, status int)
}

// ReportAuthFailure reports an upstream 401/403 for channel id.
func (g *Registry) ReportAuthFailure(id uuid.UUID, status int) {
	if g.OnAuthFailure != nil {
		g.OnAuthFailure(id, status)
	}
}

func NewRegistry(store *Store, log *slog.Logger, opts RegistryOptions) *Registry {
	if opts.RefreshInterval == 0 {
		opts.RefreshInterval = 15 * time.Second
	}
	g := &Registry{
		store: store, log: log, opts: opts, Breaker: NewBreaker(3, 30*time.Second),
		guarded: &http.Client{Transport: netguard.NewTransport(netguard.Options{Proxy: opts.Proxy}), CheckRedirect: netguard.NoRedirect},
		open:    &http.Client{Transport: netguard.NewTransport(netguard.Options{AllowPrivate: true, Proxy: opts.Proxy}), CheckRedirect: netguard.NoRedirect},
		byID:    map[uuid.UUID]*Runtime{},
		reload:  make(chan struct{}, 1),
	}
	if opts.Transport != nil {
		g.guarded = &http.Client{Transport: opts.Transport, CheckRedirect: netguard.NoRedirect}
		g.open = g.guarded
	}
	return g
}

// Reload rebuilds the snapshot from the database.
func (g *Registry) Reload(ctx context.Context) error {
	if g.opts.Plugins != nil {
		// Keeps plugin enable/disable in sync across instances.
		if err := g.opts.Plugins.RefreshStatus(ctx); err != nil {
			return err
		}
	}
	rows, err := g.store.loadRuntime(ctx)
	if err != nil {
		return err
	}
	// Group shares are expanded into their members at snapshot time; group
	// membership changes invalidate the registry.
	groupMembers, err := g.store.groupShareMembers(ctx)
	if err != nil {
		return err
	}
	all := make([]*Runtime, 0, len(rows))
	byID := make(map[uuid.UUID]*Runtime, len(rows))
	for _, row := range rows {
		rt := &Runtime{Channel: row.Channel, APIKey: row.APIKey, PluginSecrets: row.PluginSecrets, shared: map[uuid.UUID]bool{}, upstream: map[string]string{}}
		if row.PluginVersionID != nil && g.opts.Plugins != nil {
			l, err := g.opts.Plugins.Loaded(ctx, *row.PluginVersionID)
			if err != nil || l.Approval != "approved" {
				g.log.Warn("channel skipped: plugin version unavailable", "channel_id", row.ID, "err", err)
				continue
			}
			if !l.Builtin {
				rt.Plugin = l
			}
		}
		// Only accepted user shares and group shares count, and none while
		// the owner is disabled (phase5-api.md §5.4).
		if row.OwnerActive {
			for _, id := range row.acceptedUsers() {
				rt.shared[id] = true
			}
			for _, id := range groupMembers[row.ID] {
				rt.shared[id] = true
			}
		}
		for _, m := range row.Models {
			rt.upstream[m.Model] = m.UpstreamModel
		}
		rt.platform = IsPlatformRole(identity.Role(row.OwnerRole))
		ownerCanManage := (&authz.Principal{Role: identity.Role(row.OwnerRole)}).Can(authz.ChannelsManage)
		rt.AllowPrivate = g.opts.AllowPrivateNetwork && ownerCanManage
		all = append(all, rt)
		byID[rt.ID] = rt
	}
	g.mu.Lock()
	g.all, g.byID = all, byID
	g.mu.Unlock()
	if g.OnReload != nil {
		g.OnReload()
	}
	return nil
}

// Invalidate requests an asynchronous reload (after a control-plane change).
func (g *Registry) Invalidate() {
	select {
	case g.reload <- struct{}{}:
	default:
	}
}

// Run reloads periodically and on Invalidate until ctx is done.
func (g *Registry) Run(ctx context.Context) {
	t := time.NewTicker(g.opts.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-g.reload:
		}
		if err := g.Reload(ctx); err != nil && ctx.Err() == nil {
			g.log.Error("channel registry reload failed", "err", err)
		}
	}
}

// Candidates returns enabled channels usable by userID that serve model,
// optionally restricted to allowed channel IDs, ordered by priority (desc).
func (g *Registry) Candidates(userID uuid.UUID, model string, allowed []uuid.UUID) []*Runtime {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []*Runtime
	for _, rt := range g.all {
		if _, ok := rt.upstream[model]; !ok || !rt.UsableBy(userID) || !g.pluginEnabled(rt) {
			continue
		}
		if len(allowed) > 0 && !slices.Contains(allowed, rt.ID) {
			continue
		}
		out = append(out, rt)
	}
	slices.SortStableFunc(out, func(a, b *Runtime) int { return b.Priority - a.Priority })
	return out
}

// Models returns logical models usable by userID with the number of channels.
func (g *Registry) Models(userID uuid.UUID, allowed []uuid.UUID) map[string]int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := map[string]int{}
	for _, rt := range g.all {
		if !rt.UsableBy(userID) || !g.pluginEnabled(rt) || (len(allowed) > 0 && !slices.Contains(allowed, rt.ID)) {
			continue
		}
		for m := range rt.upstream {
			out[m]++
		}
	}
	return out
}

// Active returns the enabled channels whose plugin (if any) is enabled, i.e.
// every channel that can serve traffic (read-only).
func (g *Registry) Active() []*Runtime {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*Runtime, 0, len(g.all))
	for _, rt := range g.all {
		if g.pluginEnabled(rt) {
			out = append(out, rt)
		}
	}
	return out
}

// ModelNames returns the logical models the channel serves.
func (r *Runtime) ModelNames() []string {
	out := make([]string, 0, len(r.upstream))
	for m := range r.upstream {
		out = append(out, m)
	}
	return out
}

func (g *Registry) pluginEnabled(rt *Runtime) bool {
	return rt.Plugin == nil || g.opts.Plugins == nil || g.opts.Plugins.Enabled(rt.Plugin.PluginID)
}

// Get returns the runtime of an enabled channel.
func (g *Registry) Get(id uuid.UUID) (*Runtime, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rt, ok := g.byID[id]
	return rt, ok
}

// All returns the current snapshot (read-only).
func (g *Registry) All() []*Runtime {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return slices.Clone(g.all)
}

// HTTPFor returns an HTTP doer applying rt's network policy (for plugins).
func (g *Registry) HTTPFor(rt *Runtime) engine.HTTPDoer {
	return doerFunc(func(req *http.Request) (*http.Response, error) { return g.Do(rt, req) })
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

// Do sends req with the client matching the channel's network policy.
func (g *Registry) Do(rt *Runtime, req *http.Request) (*http.Response, error) {
	if rt.AllowPrivate {
		return g.open.Do(req)
	}
	if g.opts.Proxy != nil {
		if err := netguard.CheckHost(req.Context(), req.URL.Hostname()); err != nil {
			return nil, err
		}
	}
	return g.guarded.Do(req)
}

// AllModels returns every logical model served by any enabled channel
// (price management needs models from channels the manager cannot use).
func (g *Registry) AllModels() map[string]int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := map[string]int{}
	for _, rt := range g.all {
		for m := range rt.upstream {
			out[m]++
		}
	}
	return out
}
