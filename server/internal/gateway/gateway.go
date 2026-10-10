// Package gateway is the data plane (/v1/*): key authentication, policy,
// routing with fallback, protocol conversion, streaming, metering and billing.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/affinity"
	"omnigate/internal/apperr"
	"omnigate/internal/channel"
	"omnigate/internal/clientdetect"
	"omnigate/internal/identity"
	"omnigate/internal/journal"
	"omnigate/internal/keys"
	"omnigate/internal/limits"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/platform/netguard"
	"omnigate/internal/plugin"
	"omnigate/internal/plugin/engine"
	"omnigate/internal/pricing"
	"omnigate/internal/protocol"
	"omnigate/internal/requestlog"
	"omnigate/internal/routing"
	"omnigate/internal/subscription"
	"omnigate/internal/usergroup"
)

// Quota is implemented by internal/subscription.Service.
type Quota interface {
	Decide(ctx context.Context, userID uuid.UUID, model, keyOverflow string, now time.Time) (*subscription.Decision, error)
	// RecordUsage counts a finished request against the subscription's rules
	// (custom meters receive the request's BillingCtx, phase9-api.md §3).
	RecordUsage(ctx context.Context, in subscription.RecordInput) error
}

// Billing is implemented by internal/billing.Service. SettleUsage runs
// record (the usage counters) in the settlement transaction.
type Billing interface {
	Reserve(ctx context.Context, userID uuid.UUID, requestID string, estimate money.Amount) error
	SettleUsage(ctx context.Context, userID uuid.UUID, requestID string, charge money.Amount, record func(context.Context, db.Querier) error) error
}

// Limits supplies runtime limits from the system settings
// (gateway.maxAttempts, gateway.retryOn).
type Limits interface {
	GatewayMaxAttempts(ctx context.Context) int
	GatewayRetryOn(ctx context.Context) []string
}

type Options struct {
	UserAgent    string
	MaxBodyBytes int64         // request body limit (default 32 MiB)
	MaxAttempts  int           // upstream attempts per request (default 3; Limits wins when set)
	StreamIdle   time.Duration // abort a stream after this long without data (default 5m)
	MaxRespBytes int64         // non-streaming upstream body limit (default 64 MiB)
	// MaxImageBodyBytes is the image endpoint request body limit (default 64 MiB).
	MaxImageBodyBytes int64
	// Limits reads maxAttempts from system settings (optional).
	Limits Limits
	// Routes supplies administrator route rules (optional; nil = no rules).
	Routes *routing.Service
	// Usage applies user groups (price multiplier, rpm / rpd, spend limits)
	// and API key spend limits (optional; nil = multiplier 1, no limits).
	Usage *limits.Service
	// Journal receives settlements that keep failing (ADR-0010; nil: they
	// are logged as errors and lost).
	Journal *journal.Journal
	// Affinity pins client sessions to channels and passes session headers
	// through (phase12-api.md; nil = off).
	Affinity *affinity.Service
}

type Gateway struct {
	keys    *keys.Service
	reg     *channel.Registry
	prices  *pricing.Service
	billing Billing
	quota   Quota
	logs    *requestlog.Writer
	log     *slog.Logger
	opts    Options
	wg      sync.WaitGroup
	latency *routing.Latency
}

func New(k *keys.Service, reg *channel.Registry, prices *pricing.Service, b Billing, q Quota, logs *requestlog.Writer, log *slog.Logger, o Options) *Gateway {
	if o.MaxBodyBytes == 0 {
		o.MaxBodyBytes = 32 << 20
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 3
	}
	if o.StreamIdle == 0 {
		o.StreamIdle = 5 * time.Minute
	}
	if o.MaxRespBytes == 0 {
		o.MaxRespBytes = 64 << 20
	}
	if o.MaxImageBodyBytes == 0 {
		o.MaxImageBodyBytes = defaultImageBodyBytes
	}
	g := &Gateway{keys: k, reg: reg, prices: prices, billing: b, quota: q, logs: logs, log: log, opts: o, latency: routing.NewLatency()}
	if o.Routes != nil {
		g.latency = o.Routes.Latency
	}
	return g
}

// maxAttempts returns the effective per-request attempt limit without a route rule.
func (g *Gateway) maxAttempts(ctx context.Context) int {
	if g.opts.Limits != nil {
		if m := g.opts.Limits.GatewayMaxAttempts(ctx); m > 0 {
			return m
		}
	}
	return g.opts.MaxAttempts
}

// retryOn returns the retry classes used when no route rule matches.
func (g *Gateway) retryOn(ctx context.Context) []string {
	if g.opts.Limits != nil {
		if r := g.opts.Limits.GatewayRetryOn(ctx); r != nil {
			return r
		}
	}
	return routing.DefaultRetryClasses
}

// match returns the route rule for model and role (nil when none).
func (g *Gateway) match(model string, role identity.Role) *routing.Rule {
	if g.opts.Routes == nil {
		return nil
	}
	return g.opts.Routes.Match(model, role)
}

// Wait blocks until background settlement goroutines finish (shutdown, tests).
func (g *Gateway) Wait() { g.wg.Wait() }

func (g *Gateway) Routes(r chi.Router) {
	r.Get("/models", g.models)
	r.Post("/chat/completions", g.handle(protocol.OpenAIChat))
	r.Post("/completions", g.handle(protocol.OpenAICompletions))
	r.Post("/responses", g.handle(protocol.OpenAIResponses))
	r.Post("/embeddings", g.handle(protocol.OpenAIEmbeddings))
	r.Post("/images/generations", g.handle(protocol.OpenAIImagesGenerations))
	r.Post("/images/edits", g.handle(protocol.OpenAIImagesEdits))
	r.Post("/images/variations", g.handle(protocol.OpenAIImagesVariations))
	r.Post("/audio/transcriptions", g.handle(protocol.OpenAIAudioTranscriptions))
	r.Post("/audio/translations", g.handle(protocol.OpenAIAudioTranslations))
	r.Post("/audio/speech", g.handle(protocol.OpenAIAudioSpeech))
	r.Post("/messages", g.handle(protocol.Anthropic))
	r.Post("/messages/count_tokens", g.countTokens)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, dialectFor(r), protocol.NewError(protocol.ErrInvalidRequest, "unknown endpoint "+r.URL.Path))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		e := protocol.NewError(protocol.ErrInvalidRequest, "method not allowed")
		e.Status = http.StatusMethodNotAllowed
		writeError(w, r, dialectFor(r), e)
	})
}

func dialectFor(r *http.Request) string {
	if strings.Contains(r.URL.Path, "/messages") || r.Header.Get("anthropic-version") != "" {
		return protocol.Anthropic
	}
	return protocol.OpenAIChat
}

func writeError(w http.ResponseWriter, _ *http.Request, dialect string, e *protocol.GatewayError) {
	w.Header().Set("Content-Type", "application/json")
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(e.RetryAfter.Seconds()))))
	}
	w.WriteHeader(e.Status)
	_, _ = w.Write(protocol.EncodeError(dialect, e))
}

func presentedKey(r *http.Request) string {
	if v := r.Header.Get("x-api-key"); v != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// authenticate applies key, IP allowlist and RPM checks.
func (g *Gateway) authenticate(r *http.Request) (*keys.Auth, *protocol.GatewayError) {
	a, err := g.keys.Authenticate(r.Context(), presentedKey(r))
	if err != nil {
		g.log.ErrorContext(r.Context(), "key lookup failed", "err", err)
		return nil, protocol.NewError(protocol.ErrInternal, "internal error")
	}
	if a == nil {
		return nil, protocol.NewError(protocol.ErrAuthentication, "invalid or missing OmniGate API key")
	}
	if d := a.UserDisabled; d != nil {
		msg := "account disabled"
		if d.Reason != "" {
			msg += ": " + d.Reason
		}
		if d.Until != nil {
			msg += " (until " + d.Until.UTC().Format(time.RFC3339) + ")"
		}
		return nil, protocol.NewError(protocol.ErrAccountDisabled, msg)
	}
	if !a.Key.Policy.AllowsIP(httpx.ClientIP(r.Context())) {
		return nil, protocol.NewError(protocol.ErrPermission, "this API key is not allowed from your IP address")
	}
	if !g.keys.AllowRequest(a.Key) {
		return nil, protocol.NewError(protocol.ErrRateLimited, "API key rate limit (RPM) exceeded")
	}
	go g.keys.Touch(context.WithoutCancel(r.Context()), a.Key.ID)
	return a, nil
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	dialect := protocol.OpenAIChat
	if r.Header.Get("anthropic-version") != "" {
		dialect = protocol.Anthropic
	}
	a, gerr := g.authenticate(r)
	if gerr != nil {
		writeError(w, r, dialect, gerr)
		return
	}
	var names []string
	for m := range g.reg.Models(a.UserID, a.Key.Policy.AllowedChannels) {
		if a.Key.Policy.AllowsModel(m) {
			names = append(names, m)
		}
	}
	slices.Sort(names)
	now := time.Now().UTC()
	var body map[string]any
	if dialect == protocol.Anthropic {
		data := make([]map[string]any, len(names))
		for i, n := range names {
			data[i] = map[string]any{"type": "model", "id": n, "display_name": n, "created_at": now.Format(time.RFC3339)}
		}
		body = map[string]any{"data": data, "has_more": false, "first_id": first(names), "last_id": last(names)}
	} else {
		data := make([]map[string]any, len(names))
		for i, n := range names {
			data[i] = map[string]any{"id": n, "object": "model", "created": now.Unix(), "owned_by": "omnigate"}
		}
		body = map[string]any{"object": "list", "data": data}
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// countTokens serves Anthropic's token counting endpoint: forwarded to an
// Anthropic channel when one serves the model, otherwise estimated (clearly a
// heuristic, ≈4 bytes/token) because OpenAI-compatible upstreams have no
// equivalent endpoint. Not billed.
func (g *Gateway) countTokens(w http.ResponseWriter, r *http.Request) {
	a, gerr := g.authenticate(r)
	if gerr != nil {
		writeError(w, r, protocol.Anthropic, gerr)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.opts.MaxBodyBytes))
	if err != nil {
		e := protocol.NewError(protocol.ErrInvalidRequest, "failed to read request body")
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			e.Status = http.StatusRequestEntityTooLarge
		}
		writeError(w, r, protocol.Anthropic, e)
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" {
		writeError(w, r, protocol.Anthropic, protocol.NewError(protocol.ErrInvalidRequest, "missing model"))
		return
	}
	if !a.Key.Policy.AllowsModel(req.Model) {
		writeError(w, r, protocol.Anthropic, protocol.NewError(protocol.ErrPermission, "model not allowed for this key"))
		return
	}
	cands := g.reg.Candidates(a.UserID, req.Model, a.Key.Policy.AllowedChannels)
	if len(cands) == 0 {
		writeError(w, r, protocol.Anthropic, protocol.NewError(protocol.ErrModelNotFound, fmt.Sprintf("model %q is not available for this API key", req.Model)))
		return
	}
	for _, rt := range order(cands, protocol.Anthropic, req.Model) {
		if rt.Type != channel.TypeAnthropic || !g.reg.Breaker.Allow(rt.ID) {
			continue
		}
		up, _ := rt.UpstreamModel(req.Model)
		out, err := protocol.RewriteForPassthrough(protocol.Anthropic, body, up, false)
		if err != nil {
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), rt.Timeout())
		ureq, err := rt.NewRequest(ctx, protocol.Anthropic, out, r.Header, g.opts.UserAgent)
		if err == nil {
			ureq.URL.Path += "/count_tokens"
			if resp, err := g.reg.Do(rt, ureq); err == nil {
				raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				g.reg.Breaker.Release(rt.ID)
				var probe struct {
					InputTokens *int64 `json:"input_tokens"`
				}
				if resp.StatusCode == http.StatusOK && json.Unmarshal(raw, &probe) == nil && probe.InputTokens != nil {
					cancel()
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
					return
				}
			} else {
				g.reg.Breaker.Release(rt.ID)
			}
		} else {
			g.reg.Breaker.Release(rt.ID)
		}
		cancel()
		break
	}
	w.Header().Set("X-OmniGate-Estimated", "true")
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"input_tokens": protocol.EstimateTokens(len(body))})
}

func first(s []string) any {
	if len(s) == 0 {
		return nil
	}
	return s[0]
}

func last(s []string) any {
	if len(s) == 0 {
		return nil
	}
	return s[len(s)-1]
}

// order sorts candidates for one request without a route rule: priority
// (desc) first — the administrator's explicit choice — then protocol fit
// (channels that need no conversion, then one conversion step, then two), then
// a weighted shuffle within each group (see routing.Order).
func order(cands []*channel.Runtime, client, model string) []*channel.Runtime {
	rc := make([]routing.Candidate, len(cands))
	for i, c := range cands {
		rc[i] = routing.Candidate{Priority: c.Priority, Weight: c.Weight, Hops: protocol.ConversionHops(client, c.Dialect(client, model))}
	}
	out := make([]*channel.Runtime, len(cands))
	for i, j := range routing.Order(routing.StrategyPriority, routing.PreferNative, rc, 0, rand.Float64) {
		out[i] = cands[j]
	}
	return out
}

// routeCand is a candidate channel with route-rule overrides applied.
type routeCand struct {
	rt       *channel.Runtime
	tier     channel.Tier // relative to the requesting user
	priority int
	weight   int
	upstream string // upstream dialect for this request
	hops     int
}

// candidates returns the channels that may serve model: the user's usable
// channels (narrowed by the key policy), each tagged with its tier
// (docs/contracts/phase5-api.md §1). A route rule only affects the platform
// tier: its targets restrict platform channels (a rule never widens access)
// and override their priority/weight; own and shared channels are always
// candidates. Channels that cannot serve the client dialect at all are
// returned separately.
func (g *Gateway) candidates(userID uuid.UUID, allowed []uuid.UUID, model, dialect string, rule *routing.Rule) (usable, unsupported []routeCand) {
	all := g.reg.Candidates(userID, model, allowed)
	if rule != nil && len(rule.Targets) > 0 {
		byID := make(map[uuid.UUID]*channel.Runtime, len(all))
		var kept []*channel.Runtime
		for _, rt := range all {
			if rt.TierFor(userID) == channel.TierPlatform {
				byID[rt.ID] = rt
			} else {
				kept = append(kept, rt)
			}
		}
		for _, t := range rule.Targets { // target order is the round-robin order
			if rt, ok := byID[t.ChannelID]; ok {
				kept = append(kept, rt)
			}
		}
		all = kept
	}
	for _, rt := range all {
		c := routeCand{rt: rt, tier: rt.TierFor(userID), priority: rt.Priority, weight: rt.Weight, upstream: rt.Dialect(dialect, model)}
		c.hops = protocol.ConversionHops(dialect, c.upstream)
		if rule != nil && c.tier == channel.TierPlatform {
			if t, ok := rule.TargetFor(rt.ID); ok {
				if t.Priority != nil {
					c.priority = *t.Priority
				}
				if t.Weight != nil {
					c.weight = *t.Weight
				}
			}
		}
		if rt.Supports(dialect) {
			usable = append(usable, c)
		} else {
			unsupported = append(unsupported, c)
		}
	}
	return usable, unsupported
}

// arrange returns the attempt order: own → shared → platform. Own and shared
// channels use the default order (priority, protocol fit, weight); the
// platform tier is ranked by the rule's strategy.
func (g *Gateway) arrange(ctx context.Context, cands []routeCand, model string, rule *routing.Rule, at time.Time, advance bool) []routeCand {
	out := make([]routeCand, 0, len(cands))
	for _, tier := range channel.Tiers {
		var group []routeCand
		for _, c := range cands {
			if c.tier == tier {
				group = append(group, c)
			}
		}
		if len(group) == 0 {
			continue
		}
		if tier == channel.TierPlatform {
			out = append(out, g.rank(ctx, group, model, rule, at, advance)...)
		} else {
			out = append(out, g.rank(ctx, group, model, nil, at, false)...)
		}
	}
	return out
}

// latencyOf returns the channel's TTFB average for model in ms (nil = no data).
func (g *Gateway) latencyOf(id uuid.UUID, model string) *float64 {
	if v, ok := g.latency.Get(id, model); ok {
		return &v
	}
	return nil
}

// costOf returns the channel's input + output cost price per million tokens
// for model (looked up like settlement: upstream model + channel), nil if none.
func (g *Gateway) costOf(ctx context.Context, rt *channel.Runtime, model string, at time.Time) *money.Amount {
	up, _ := rt.UpstreamModel(model)
	p, err := g.prices.Lookup(ctx, pricing.KindCost, up, &rt.ID, at)
	if err != nil || p == nil {
		return nil
	}
	sum, err := p.InputPerM.Add(p.OutputPerM)
	if err != nil {
		return nil
	}
	if sum, err = p.ScheduleMultiplier(at).Apply(sum); err != nil {
		return nil
	}
	return &sum
}

// rank orders candidates by the rule's strategy (default: priority with
// native protocol first). advance=false peeks the round-robin counter.
func (g *Gateway) rank(ctx context.Context, cands []routeCand, model string, rule *routing.Rule, at time.Time, advance bool) []routeCand {
	strategy, pref := routing.StrategyPriority, routing.PreferNative
	var rr uint64
	if rule != nil {
		strategy, pref = rule.Strategy, rule.ProtocolPreference
		if strategy == routing.StrategyRoundRobin {
			if advance {
				rr = g.opts.Routes.NextRoundRobin(rule.ID)
			} else {
				rr = g.opts.Routes.PeekRoundRobin(rule.ID)
			}
		}
	}
	rc := make([]routing.Candidate, len(cands))
	for i, c := range cands {
		rc[i] = routing.Candidate{Priority: c.priority, Weight: c.weight, Hops: c.hops}
		switch strategy {
		case routing.StrategyLeastLatency:
			rc[i].LatencyMs = g.latencyOf(c.rt.ID, model)
		case routing.StrategyLowestCost:
			rc[i].Cost = g.costOf(ctx, c.rt, model, at)
		}
	}
	out := make([]routeCand, len(cands))
	for i, j := range routing.Order(strategy, pref, rc, rr, rand.Float64) {
		out[i] = cands[j]
	}
	return out
}

// reqState carries everything about one gateway request.
type reqState struct {
	dialect  string
	start    time.Time
	auth     *keys.Auth
	info     protocol.RequestInfo
	body     []byte
	compat   protocol.Compat
	entry    *requestlog.Entry
	written  bool
	warnings []string
	// subscription covering this request (wallet is skipped when set)
	subscription *uuid.UUID
	// reserved is set once balance was reserved for the request.
	reserved bool
	// multipart is the spooled body of a multipart image request (body is nil).
	multipart *multipartBody
	// group is the key owner's user group (nil without Options.Usage).
	group *usergroup.Group
	// limited is set once the request limits were checked (before the first
	// attempt); counted when the request passed them (it counts for rpd).
	limited, counted bool
	// abort is set when a committed binary response broke off: the connection
	// is aborted after the request was settled and logged (audio.go).
	abort bool
	// affinity is the request's session affinity state (nil = no rule applied).
	affinity *affinity.Session
	// client is the client detected from the request headers (once per
	// request; affinity client_include and the log entry use it).
	client clientdetect.Client
}

// groupMultiplier is the sell-price multiplier of the request's user group.
func (st *reqState) groupMultiplier() pricing.Multiplier {
	if st.group == nil {
		return pricing.One
	}
	return st.group.Multiplier
}

// cleanup releases request resources (the spooled image body).
func (st *reqState) cleanup() {
	if st.multipart != nil {
		st.multipart.spool.close()
	}
}

// switchModel rewrites the request for a fallback model (before any byte was
// sent to the client).
func (st *reqState) switchModel(model string) error {
	if st.multipart != nil {
		// The multipart encoder writes the channel's upstream model of st.info.Model.
		st.info.Model = model
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(st.body, &m); err != nil {
		return err
	}
	m["model"], _ = json.Marshal(model)
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	st.body, st.info.Model = b, model
	return nil
}

func (g *Gateway) handle(dialect string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The whole request counts as in-flight work for Wait: settlement
		// goroutines started in finish are then always added while the
		// counter is positive (never racing a concurrent Wait), and Wait also
		// covers requests whose client already saw the response end.
		g.wg.Add(1)
		defer g.wg.Done()
		st := &reqState{dialect: dialect, start: time.Now().UTC()}
		reqID := httpx.RequestID(r.Context())
		st.entry = &requestlog.Entry{ID: uuid.Must(uuid.NewV7()), StartedAt: st.start, RequestID: reqID, Inbound: dialect,
			IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context()))}
		// Only the client id and version are logged, never the raw headers.
		st.client = clientdetect.Detect(r.Header)
		st.entry.Client = &st.client.ID
		if st.client.Version != "" {
			st.entry.ClientVersion = &st.client.Version
		}
		defer st.cleanup()
		gerr := g.serve(w, r, st)
		if gerr != nil && !st.written {
			writeError(w, r, dialect, gerr)
		}
		g.finish(st, gerr)
		if st.abort {
			panic(http.ErrAbortHandler)
		}
	}
}

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request, st *reqState) *protocol.GatewayError {
	a, gerr := g.authenticate(r)
	if gerr != nil {
		return gerr
	}
	st.auth = a
	st.entry.UserID, st.entry.KeyID, st.entry.KeyName = &a.UserID, &a.Key.ID, &a.Key.Name
	if g.opts.Usage != nil {
		grp, err := g.opts.Usage.Group(r.Context(), a.GroupID)
		if err != nil {
			g.log.ErrorContext(r.Context(), "user group lookup failed", "err", err)
			return protocol.NewError(protocol.ErrInternal, "internal error")
		}
		st.group = grp
	}
	if a.Key.Policy.CompatMode == "lenient" {
		st.compat = protocol.Lenient
	}

	if protocol.IsImages(st.dialect) {
		if gerr := g.readImageRequest(w, r, st); gerr != nil {
			return gerr
		}
	} else if protocol.IsAudio(st.dialect) {
		if gerr := g.readAudioRequest(w, r, st); gerr != nil {
			return gerr
		}
	} else if gerr := g.readRequest(w, r, st); gerr != nil {
		return gerr
	}
	info := st.info
	st.entry.Model, st.entry.Stream = info.Model, info.Stream
	if !a.Key.Policy.AllowsModel(info.Model) {
		return protocol.NewError(protocol.ErrPermission, fmt.Sprintf("this API key is not allowed to use model %q", info.Model))
	}
	return g.route(w, r, st)
}

// readRequest reads a JSON request body and fills st.body and st.info.
func (g *Gateway) readRequest(w http.ResponseWriter, r *http.Request, st *reqState) *protocol.GatewayError {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.opts.MaxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return tooLarge(g.opts.MaxBodyBytes)
		}
		return protocol.NewError(protocol.ErrClientClosed, "failed to read request body")
	}
	st.body = body
	info, err := protocol.ParseInfo(st.dialect, body)
	if err != nil {
		return convertError(err)
	}
	if st.dialect == protocol.OpenAIEmbeddings {
		// Embeddings never stream; drop the field so strict upstreams don't reject it.
		info.Stream = false
		var m map[string]json.RawMessage
		if json.Unmarshal(body, &m) == nil {
			if _, ok := m["stream"]; ok {
				delete(m, "stream")
				if b, err := json.Marshal(m); err == nil {
					body, st.body = b, b
				}
			}
		}
	}
	st.info = info
	return nil
}

// route tries the candidate channels (own → shared → platform, route rule
// fallback models) until one serves the request.
func (g *Gateway) route(w http.ResponseWriter, r *http.Request, st *reqState) *protocol.GatewayError {
	a, info := st.auth, st.info
	maxAttempts := g.maxAttempts(r.Context())
	retryOn := g.retryOn(r.Context())
	rule := g.match(info.Model, a.UserRole)
	// The rule's retry policy governs the platform tier; own and shared
	// channels are each tried at most once and follow the general retry classes.
	retry := routing.Retry{MaxAttempts: maxAttempts, RetryOn: retryOn}
	free := routing.Retry{MaxAttempts: routing.HardAttemptCap, RetryOn: retryOn}
	models := []string{info.Model}
	if rule != nil {
		retry = rule.Retry
		models = append(models, rule.FallbackModels...)
	}
	st.affinity = g.opts.Affinity.Begin(r.Context(), affinity.Request{UserID: a.UserID, GroupID: a.GroupID, Model: info.Model,
		Path: r.URL.Path, Dialect: st.dialect, UserAgent: r.UserAgent(), Client: st.client.ID, Header: r.Header, Body: st.body})

	var lastErr *protocol.GatewayError
	attempts := 0
	for i, model := range models {
		if attempts >= routing.HardAttemptCap {
			break
		}
		mrule := rule
		if i > 0 {
			// Fallback model: same key policy and routing as a direct request.
			if model == info.Model || !a.Key.Policy.AllowsModel(model) {
				continue
			}
			mrule = g.match(model, a.UserRole)
			if err := st.switchModel(model); err != nil {
				continue
			}
		}
		cands, _ := g.candidates(a.UserID, a.Key.Policy.AllowedChannels, model, st.dialect, mrule)
		if len(cands) == 0 {
			if i == 0 {
				st.affinity.Broken() // a bound channel cannot be a candidate
				msg := fmt.Sprintf("model %q is not available for this API key", model)
				switch {
				case st.dialect == protocol.OpenAIEmbeddings:
					msg += " (embeddings are served by OpenAI-compatible channels only)"
				case st.dialect == protocol.OpenAICompletions:
					msg += " (completions are served by OpenAI-compatible channels with Completions support enabled only)"
				case protocol.IsImages(st.dialect):
					msg += " (image endpoints are served by OpenAI-compatible channels only)"
				case protocol.IsAudio(st.dialect):
					msg += " (audio endpoints are served by OpenAI-compatible channels only)"
				}
				lastErr = protocol.NewError(protocol.ErrModelNotFound, msg)
				if len(models) == 1 {
					return lastErr
				}
			}
			continue
		}
		platformAttempts, admitted := 0, false
		ordered := g.arrange(r.Context(), cands, model, mrule, st.start, true)
		if i == 0 {
			ordered = g.pin(st, ordered) // fallback models are never pinned
		}
	attemptLoop:
		for _, c := range ordered {
			if attempts >= routing.HardAttemptCap {
				break
			}
			if !st.limited {
				// The group's rpm / rpd apply to every tier: checked once,
				// after tiering and before the first attempt (and before the
				// platform tier's quota / wallet / spend checks).
				st.limited = true
				if gerr := g.checkRequestLimits(r, st); gerr != nil {
					return gerr
				}
			}
			policy := free
			if c.tier == channel.TierPlatform {
				if platformAttempts >= retry.MaxAttempts {
					break
				}
				if !admitted {
					// Lazy billing: quotas and the wallet are only consulted
					// right before the first platform channel is tried.
					if gerr := g.admit(r, st, model); gerr != nil {
						if i == 0 {
							return gerr
						}
						break attemptLoop // the fallback model is not covered (quota / balance)
					}
					admitted = true
				}
				policy = retry
			}
			if !g.reg.Breaker.Allow(c.rt.ID) {
				continue
			}
			if c.tier == channel.TierPlatform {
				platformAttempts++
			}
			attempts++
			st.affinity.Attempted()
			gerr, class := g.attempt(w, r, st, c.rt, c.tier)
			if gerr == nil {
				st.affinity.Served(c.rt.ID, i == 0)
				return nil
			}
			lastErr = gerr
			// Strict session affinity: the bound channel's error is returned
			// without trying another channel.
			if st.affinity.StopAfter(c.rt.ID) || !policy.Allows(class) || st.written || r.Context().Err() != nil {
				return gerr
			}
		}
	}
	if lastErr == nil {
		return protocol.NewError(protocol.ErrUpstreamUnavailable, "all channels for this model are temporarily unavailable (circuit open)")
	}
	return lastErr
}

// pin puts the session's bound channel first within its tier (session
// affinity, phase12-api.md §2). The tier order own → shared → platform is a
// billing boundary and never changes. A bound channel that is not a candidate
// — disabled, no longer usable by the key, not serving the model, or with an
// open circuit — breaks the binding and the request is routed normally.
func (g *Gateway) pin(st *reqState, ordered []routeCand) []routeCand {
	id, ok := st.affinity.Bound()
	if !ok {
		return ordered
	}
	idx := slices.IndexFunc(ordered, func(c routeCand) bool { return c.rt.ID == id })
	if idx < 0 || g.reg.Breaker.State(id) == "open" {
		st.affinity.Broken()
		return ordered
	}
	start := idx
	for start > 0 && ordered[start-1].tier == ordered[idx].tier {
		start--
	}
	c := ordered[idx]
	copy(ordered[start+1:idx+1], ordered[start:idx])
	ordered[start] = c
	st.affinity.Routed()
	return ordered
}

// admit applies plan quotas (docs/contracts/phase3-api.md §3) and, for a
// request the wallet pays for (priced model, no plan), the spend limits and
// the prepaid balance check (phase4-api.md §3): admitted only while the
// available balance is > 0, independent of the estimate. The hold is the
// sell-price cost of the estimated input plus max_tokens when the request sets
// it (clamped to protocol.MaxTokensLimit; no output is assumed otherwise),
// at the context-length tier of the estimated prompt tokens (phase10-api.md §1),
// capped at the available balance and the remaining spend limit. A request
// that cannot be priced is rejected. Request state changes only when the
// model is admitted.
func (g *Gateway) admit(r *http.Request, st *reqState, model string) *protocol.GatewayError {
	ctx := r.Context()
	a := st.auth
	var sub *uuid.UUID
	if g.quota != nil {
		d, err := g.quota.Decide(ctx, a.UserID, model, a.Key.Policy.QuotaOverflow, st.start)
		if err != nil {
			g.log.ErrorContext(ctx, "quota decide failed", "err", err)
			return protocol.NewError(protocol.ErrInternal, "internal error")
		}
		if d.Blocked != nil {
			class := protocol.ErrQuotaExceeded
			if d.Blocked.Code == subscription.CodeQuotaExhausted {
				class = protocol.ErrQuotaExhausted
			}
			e := protocol.NewError(class, d.Blocked.Message)
			e.RetryAfter = d.Blocked.RetryAfter
			return e
		}
		sub = d.SubscriptionID
	}
	var sellID *uuid.UUID
	if sub == nil {
		sell, err := g.prices.Lookup(ctx, pricing.KindSell, model, nil, st.start)
		if err != nil {
			g.log.ErrorContext(ctx, "price lookup failed", "err", err)
			return protocol.NewError(protocol.ErrInternal, "internal error")
		}
		if sell != nil {
			// Spend limits (group daily / monthly, key) only constrain
			// requests the wallet pays for (phase8-api.md §2.2).
			remaining, gerr := g.checkSpend(r, st)
			if gerr != nil {
				return gerr
			}
			est := protocol.Usage{Input: protocol.EstimateTokens(len(st.body)), Output: st.info.MaxTokens}
			if protocol.IsImages(st.dialect) {
				// perRequest + n × perImage + estimated text input (phase7-api.md §1.1).
				est = protocol.Usage{Input: protocol.EstimateTokens(st.info.PromptBytes), Images: st.info.Images}
			} else if protocol.IsAudio(st.dialect) {
				est = audioEstimate(st.info) // phase9-api.md §1.1
			}
			estimate, err := sell.Compute(est, st.start, st.groupMultiplier())
			if err != nil {
				// The request cannot be priced (it would overflow): never
				// admitted, whatever the balance.
				g.log.WarnContext(ctx, "balance estimate failed", "model", model, "err", err)
				return protocol.NewError(protocol.ErrInvalidRequest, "the requested usage is too large to be priced; lower max_tokens or the request size")
			}
			if remaining != nil {
				// Like the wallet, the hold never exceeds what the limit has left.
				estimate = min(estimate, *remaining)
			}
			// Prepaid (billing.enforce): the wallet pays for this request, so it
			// is admitted only while the available balance is > 0 — whatever
			// the estimate (an estimate of 0, e.g. a per-minute audio price or
			// an output-only price without max_tokens, still needs a balance).
			// Reserve holds min(estimate, available); not enforced, it is a no-op.
			if err := g.billing.Reserve(ctx, a.UserID, st.entry.RequestID, max(estimate, 0)); err != nil {
				if apperr.As(err).Code == "insufficient_balance" {
					return protocol.NewError(protocol.ErrInsufficientBalance, "insufficient balance; please top up or redeem a code")
				}
				g.log.ErrorContext(ctx, "reserve failed", "err", err)
				return protocol.NewError(protocol.ErrInternal, "internal error")
			}
			if estimate > 0 {
				st.reserved = true
			}
			sellID = &sell.ID
		}
	}
	st.subscription, st.entry.SubscriptionID, st.entry.SellPriceID = sub, sub, sellID
	return nil
}

func convertError(err error) *protocol.GatewayError {
	var ce *protocol.ConvertError
	if errors.As(err, &ce) {
		return protocol.NewError(ce.Class, ce.Message)
	}
	return protocol.NewError(protocol.ErrInvalidRequest, err.Error())
}

// buildBody produces the upstream request body for rt.
func (st *reqState) buildBody(rt *channel.Runtime, upDialect, upstreamModel string) ([]byte, []string, error) {
	if upDialect == st.dialect {
		b, err := protocol.RewriteForPassthrough(st.dialect, st.body, upstreamModel, st.info.Stream)
		return b, nil, err
	}
	return protocol.ConvertRequest(st.dialect, upDialect, st.body, upstreamModel, rt.MaxTokensField(), st.compat)
}

// attempt tries one channel. It returns (nil, _) on success, or the error and
// its retry class (routing.Retry*; "" = another channel must not be tried).
func (g *Gateway) attempt(w http.ResponseWriter, r *http.Request, st *reqState, rt *channel.Runtime, tier channel.Tier) (*protocol.GatewayError, string) {
	if rt.Plugin != nil && rt.Plugin.CustomProtocol() {
		return g.attemptCustom(w, r, st, rt, tier) // custom.go
	}
	upDialect := rt.Dialect(st.dialect, st.info.Model)
	upstreamModel, _ := rt.UpstreamModel(st.info.Model)
	att := requestlog.Attempt{ChannelID: rt.ID, ChannelName: rt.Name}
	attStart := time.Now()
	st.entry.ChannelID, st.entry.ChannelName, st.entry.UpstreamModel = &rt.ID, &rt.Name, &upstreamModel
	st.entry.ChannelTier = string(tier)
	st.entry.ServedModel = st.info.Model
	defer func() {
		att.DurationMs = time.Since(attStart).Milliseconds()
		st.entry.Attempts = append(st.entry.Attempts, att)
	}()
	fail := func(e *protocol.GatewayError, class string, health bool) (*protocol.GatewayError, string) {
		c := e.Class
		att.ErrorClass = &c
		if att.StatusCode == 0 {
			att.StatusCode = e.Status
		}
		if health {
			g.reg.Breaker.Failure(rt.ID, e.Message)
		} else {
			g.reg.Breaker.Release(rt.ID)
		}
		return e, class
	}

	var body []byte
	if st.multipart == nil {
		var warnings []string
		var err error
		body, warnings, err = st.buildBody(rt, upDialect, upstreamModel)
		if err != nil {
			g.reg.Breaker.Release(rt.ID)
			return convertError(err), ""
		}
		st.warnings = warnings
		if k := st.affinity.PromptCacheKey(); k != "" && openAIText(upDialect) {
			// Session affinity's per-conversation prompt_cache_key (every
			// attempt, before plugin hooks so they see and sign the final body).
			if body, err = protocol.SetPromptCacheKey(body, k); err != nil {
				g.reg.Breaker.Release(rt.ID)
				return convertError(err), ""
			}
		}
	}

	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	var ov *channel.RequestOverride
	if rt.Plugin != nil && rt.Plugin.HasHooks() {
		headers := map[string]string{}
		for k, v := range rt.Config.Headers {
			headers[k] = v
		}
		env := rt.PluginEnv(g.reg.HTTPFor(rt), g.opts.UserAgent)
		hreq := plugin.UpstreamRequest{Dialect: upDialect, Path: rt.DefaultPath(upDialect), Headers: headers, Body: body}
		var out *plugin.UpstreamRequest
		var err error
		if st.multipart != nil {
			// transformRequest only applies to JSON bodies (phase7-api.md §1).
			out, err = rt.Plugin.RunSignHook(ctx, env, hreq)
		} else {
			out, err = rt.Plugin.RunHooks(ctx, env, hreq)
		}
		if err != nil {
			msg := err.Error()
			var pe *engine.Error
			if errors.As(err, &pe) {
				msg = pe.Message
			}
			return fail(protocol.NewError(protocol.ErrPluginError, "channel plugin hook failed: "+protocol.Redact(msg)), routing.RetryServerError, true)
		}
		if st.multipart == nil {
			body = out.Body
		}
		ov = &channel.RequestOverride{Path: out.Path, Headers: out.Headers, SkipAuth: rt.Plugin.SignsRequests()}
	}
	if p := st.affinity.PassHeaders(); p != nil {
		// Session headers of the applying affinity rule (every attempt).
		if ov == nil {
			ov = &channel.RequestOverride{}
		}
		ov.Pass = p
	}
	if name, v := st.affinity.SessionHeader(); name != "" && openAIText(upDialect) {
		// Session header of the applying affinity rule, unless the request
		// already carries it (channel config, plugin hook, passed client header).
		if ov == nil {
			ov = &channel.RequestOverride{}
		}
		ov.Fill = map[string]string{name: v}
	}
	var req *http.Request
	var err error
	if st.multipart != nil {
		// A fresh streamed encoding per attempt (retries replay the spooled body).
		mbody, length, stop, perr := st.multipart.stream(upstreamModel)
		if perr != nil {
			g.log.ErrorContext(r.Context(), "encode image request failed", "err", perr)
			return fail(protocol.NewError(protocol.ErrInternal, "failed to build upstream request"), "", false)
		}
		defer stop()
		req, err = rt.NewRequestBody(ctx, upDialect, mbody, st.multipart.contentType(), length, r.Header, g.opts.UserAgent, ov)
	} else {
		req, err = rt.NewRequest(ctx, upDialect, body, r.Header, g.opts.UserAgent, ov)
	}
	if err != nil {
		return fail(protocol.NewError(protocol.ErrInternal, "failed to build upstream request"), "", false)
	}
	errHeaderTimeout := errors.New("upstream header timeout")
	timer := time.AfterFunc(rt.Timeout(), func() { cancel(errHeaderTimeout) })
	resp, err := g.reg.Do(rt, req)
	timer.Stop()
	if err != nil {
		switch {
		case r.Context().Err() != nil:
			return fail(protocol.NewError(protocol.ErrClientClosed, "client closed request"), "", false)
		case errors.Is(context.Cause(ctx), errHeaderTimeout):
			return fail(protocol.NewError(protocol.ErrUpstreamTimeout, fmt.Sprintf("upstream did not respond within %s", rt.Timeout())), routing.RetryTimeout, true)
		case errors.Is(err, netguard.ErrBlocked):
			return fail(protocol.NewError(protocol.ErrUpstreamUnavailable, "upstream address blocked by network policy"), routing.RetryNetwork, true)
		default:
			return fail(protocol.NewError(protocol.ErrUpstreamUnavailable, "upstream connection failed"), routing.RetryNetwork, true)
		}
	}
	defer resp.Body.Close()
	att.StatusCode = resp.StatusCode
	ttft := time.Since(st.start).Milliseconds()
	firstByte := time.Since(attStart) // upstream time to first byte (headers)

	if resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			g.reg.ReportAuthFailure(rt.ID, resp.StatusCode)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		e, class, health := classifyStatus(resp.StatusCode, "upstream: "+protocol.UpstreamErrorMessage(raw))
		return fail(e, class, health)
	}

	var gerr *protocol.GatewayError
	var class string
	if protocol.IsAudio(st.dialect) {
		// Passthrough by response type: SSE, binary audio or JSON / text.
		gerr, class = g.audioResponse(w, r, st, rt, resp, upDialect, upstreamModel, ttft, fail)
	} else if st.info.Stream && (!protocol.IsImages(st.dialect) || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")) {
		// Image models without streaming support (dall-e-*) may answer a
		// stream request with plain JSON: that is served as a unary response.
		gerr, class = g.stream(w, r, st, rt, resp, upDialect, upstreamModel, ttft, fail)
	} else {
		gerr, class = g.unary(w, st, rt, resp, upDialect, upstreamModel, ttft, fail)
	}
	if gerr == nil {
		g.latency.Observe(rt.ID, st.info.Model, firstByte)
	}
	return gerr, class
}

// openAIText reports whether an upstream dialect is OpenAI Chat or Responses:
// the requests session affinity's inject options apply to. Completions are
// excluded on purpose (phase14-api.md §5): FIM upstreams know neither
// prompt_cache_key nor session headers, and a strict upstream may reject the
// unknown field.
func openAIText(dialect string) bool {
	return dialect == protocol.OpenAIChat || dialect == protocol.OpenAIResponses
}

// classifyStatus maps an upstream error status to the gateway error, its retry
// class ("" = do not try another channel) and whether it counts against the
// channel's health.
func classifyStatus(status int, msg string) (*protocol.GatewayError, string, bool) {
	switch {
	case status == 401 || status == 403:
		return protocol.NewError(protocol.ErrUpstreamAuth, "upstream rejected the channel credentials"), routing.RetryAuthError, true
	case status == 429:
		return protocol.NewError(protocol.ErrUpstreamRateLimited, msg), routing.RetryRateLimit, true
	case status == 408:
		return protocol.NewError(protocol.ErrUpstreamTimeout, msg), routing.RetryTimeout, true
	case status >= 500:
		return protocol.NewError(protocol.ErrUpstreamUnavailable, msg), routing.RetryServerError, true
	case status == 404:
		// Often a channel-specific model mapping problem: try another channel.
		return protocol.NewError(protocol.ErrModelNotFound, msg), routing.RetryNotFound, false
	case status < 400:
		return protocol.NewError(protocol.ErrUpstreamInvalid, "unexpected upstream redirect"), routing.RetryServerError, true
	default:
		// Other 4xx: retried only when client_error is enabled (not by default),
		// and never counted against the channel's health.
		e := protocol.NewError(protocol.ErrUpstreamBadRequest, msg)
		e.Status = status
		if status == 422 {
			e.Status = http.StatusBadRequest
		}
		return e, routing.RetryClientError, false
	}
}

type failFunc func(*protocol.GatewayError, string, bool) (*protocol.GatewayError, string)

func (g *Gateway) writeHeaders(w http.ResponseWriter, st *reqState, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	if len(st.warnings) > 0 {
		h.Set("X-OmniGate-Compat-Warnings", strings.Join(st.warnings, ","))
	}
	if strings.HasPrefix(contentType, "text/event-stream") {
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(http.StatusOK)
	st.written = true
}

func (g *Gateway) unary(w http.ResponseWriter, st *reqState, rt *channel.Runtime, resp *http.Response, upDialect, upstreamModel string, ttft int64, fail failFunc) (*protocol.GatewayError, string) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, g.opts.MaxRespBytes+1))
	if err != nil {
		return fail(protocol.NewError(protocol.ErrUpstreamUnavailable, "failed to read upstream response"), routing.RetryNetwork, true)
	}
	if int64(len(raw)) > g.opts.MaxRespBytes {
		return fail(protocol.NewError(protocol.ErrUpstreamInvalid, "upstream response too large"), "", false)
	}
	var out []byte
	var usage protocol.Usage
	var ok bool
	switch {
	case protocol.IsImages(st.dialect):
		// Token usage when reported (gpt-image-*), otherwise only the image
		// count is billed (never estimated).
		out = raw
		usage, _, err = protocol.UsageFromImagesResponse(raw)
	case upDialect == st.dialect:
		out = raw
		switch upDialect {
		case protocol.OpenAIChat, protocol.OpenAIEmbeddings:
			usage, ok = protocol.UsageFromChatResponse(raw)
		case protocol.Anthropic:
			usage, ok = protocol.UsageFromAnthropicResponse(raw)
		case protocol.OpenAIResponses:
			usage, ok = protocol.UsageFromResponsesResponse(raw)
		case protocol.OpenAICompletions:
			usage, ok = protocol.UsageFromCompletionResponse(raw)
		}
		if !ok {
			usage = protocol.Usage{Input: protocol.EstimateTokens(len(st.body)), Output: protocol.EstimateTokens(len(raw)), Estimated: true}
		}
	default:
		out, usage, err = protocol.ConvertResponse(st.dialect, upDialect, raw, st.info.Model)
	}
	if err != nil {
		return fail(protocol.NewError(protocol.ErrUpstreamInvalid, "upstream returned a malformed response"), routing.RetryServerError, true)
	}
	g.reg.Breaker.Success(rt.ID)
	g.writeHeaders(w, st, "application/json")
	_, _ = w.Write(out)
	st.entry.StatusCode = http.StatusOK
	st.entry.TTFTMs = &ttft
	st.entry.Usage = usage
	return nil, ""
}

func (g *Gateway) stream(w http.ResponseWriter, r *http.Request, st *reqState, rt *channel.Runtime, resp *http.Response, upDialect, upstreamModel string, ttft int64, fail failFunc) (*protocol.GatewayError, string) {
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return fail(protocol.NewError(protocol.ErrUpstreamInvalid, "upstream did not return an event stream (content-type "+ct+")"), routing.RetryServerError, true)
	}
	var proc protocol.StreamProcessor
	var err error
	if protocol.IsImages(st.dialect) {
		proc = protocol.NewImageStreamProcessor()
	} else if protocol.IsAudio(st.dialect) {
		proc = protocol.NewAudioStreamProcessor(st.dialect, st.info.Characters)
	} else {
		proc, err = protocol.NewStreamProcessor(st.dialect, upDialect, st.info.Model, st.info.IncludeUsage)
	}
	if err != nil {
		return fail(protocol.NewError(protocol.ErrInternal, err.Error()), "", false)
	}
	flusher, _ := w.(http.Flusher)

	// Abort if the upstream goes silent for StreamIdle.
	idle := time.AfterFunc(g.opts.StreamIdle, func() { resp.Body.Close() })
	defer idle.Stop()

	reader := protocol.NewSSEReader(resp.Body)
	var firstByte *int64
	emit := func(b []byte) bool {
		if len(b) == 0 {
			return true
		}
		if !st.written {
			g.writeHeaders(w, st, "text/event-stream; charset=utf-8")
			t := time.Since(st.start).Milliseconds()
			firstByte = &t
		}
		if _, err := w.Write(b); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	var streamErr *protocol.GatewayError
	streamClass := routing.RetryServerError // malformed or incomplete stream
	for {
		ev, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil && proc.Done() {
			// The terminal event already reached the client; clients such as
			// Codex hang up right after it. That is a complete response.
			break
		}
		if err != nil {
			if r.Context().Err() != nil {
				streamErr = protocol.NewError(protocol.ErrClientClosed, "client closed stream")
			} else {
				streamErr = protocol.NewError(protocol.ErrUpstreamInvalid, "upstream stream interrupted")
				streamClass = routing.RetryNetwork
			}
			break
		}
		idle.Reset(g.opts.StreamIdle)
		if ev.IsComment() && upDialect != st.dialect {
			continue
		}
		out, perr := proc.Process(ev)
		if perr != nil {
			streamErr = protocol.NewError(protocol.ErrUpstreamInvalid, perr.Error())
			break
		}
		if !emit(out) {
			if !proc.Done() {
				streamErr = protocol.NewError(protocol.ErrClientClosed, "client closed stream")
			}
			break
		}
	}
	if streamErr == nil {
		emit(proc.Finish())
		if !proc.Done() {
			streamErr = protocol.NewError(protocol.ErrUpstreamInvalid, "upstream stream ended without a terminal event")
		}
	}

	usage, ok := proc.Usage()
	if !protocol.IsImages(st.dialect) && !protocol.IsAudio(st.dialect) && (!ok || (usage.Output == 0 && proc.OutputBytes() > 0)) {
		if !ok {
			usage.Input = protocol.EstimateTokens(len(st.body))
		}
		usage.Output = protocol.EstimateTokens(proc.OutputBytes())
		usage.Estimated = true
	}

	if streamErr != nil && !st.written {
		// Nothing reached the client: safe to try another channel.
		if streamErr.Class == protocol.ErrClientClosed {
			return fail(streamErr, "", false)
		}
		return fail(streamErr, streamClass, true)
	}
	if streamErr != nil && streamErr.Class != protocol.ErrClientClosed {
		emit(protocol.EncodeStreamError(st.dialect, streamErr))
		g.reg.Breaker.Failure(rt.ID, streamErr.Message)
	} else {
		g.reg.Breaker.Success(rt.ID)
	}
	st.entry.StatusCode = http.StatusOK
	if streamErr != nil {
		c, m := streamErr.Class, streamErr.Message
		st.entry.ErrorClass, st.entry.ErrorMessage = &c, &m
		if c == protocol.ErrClientClosed {
			st.entry.StatusCode = 499
		}
	}
	if firstByte != nil {
		st.entry.TTFTMs = firstByte
	} else {
		st.entry.TTFTMs = &ttft
	}
	st.entry.Usage = usage
	// The response is committed: report success to the caller even on a
	// mid-stream error (it was delivered in-band and logged above).
	return nil, ""
}

// finish prices the request, settles billing and writes the log entry.
func (g *Gateway) finish(st *reqState, gerr *protocol.GatewayError) {
	e := st.entry
	e.DurationMs = time.Since(st.start).Milliseconds()
	if gerr != nil {
		c, m := gerr.Class, gerr.Message
		e.ErrorClass, e.ErrorMessage, e.StatusCode = &c, &m, gerr.Status
	}
	if o := st.affinity.Outcome(); o != "" {
		// The rule name and outcome only; never the session value.
		rule := st.affinity.Rule
		e.Affinity, e.AffinityRule = &o, &rule
	}
	if e.ChannelTier == string(channel.TierOwn) || e.ChannelTier == string(channel.TierShared) {
		// Own and shared channels are never billed (phase5-api.md §1): no
		// charge, no quota usage, no cost; a hold taken for a platform attempt
		// of this request is released.
		e.Charge, e.QuotaCharge, e.Cost = 0, 0, 0
		e.SellPriceID, e.CostPriceID, e.SubscriptionID = nil, nil, nil
		g.settle(st, 0, st.reserved, nil)
		g.logs.Add(e)
		return
	}
	succeeded := gerr == nil && e.StatusCode > 0 && e.StatusCode < 500 || (e.StatusCode == 499 && !e.Usage.IsZero())
	ctx := context.Background()
	// Billing and quotas follow the model that actually served the request.
	served := e.Model
	if e.ServedModel != "" {
		served = e.ServedModel
	}
	if succeeded && e.ChannelID != nil {
		if sell, err := g.prices.Lookup(ctx, pricing.KindSell, served, nil, st.start); err == nil && sell != nil {
			// Sell price × schedule multiplier at the request start × the
			// user group's multiplier (phase8-api.md §1.1, §3).
			group := st.groupMultiplier()
			e.SellPriceID = &sell.ID
			var err error
			if e.Charge, err = sell.Compute(e.Usage, st.start, group); err != nil {
				g.log.Error("charge computation failed: request not charged", "request_id", e.RequestID, "model", served, "err", err)
			}
			m := pricing.Product(sell.ScheduleMultiplier(st.start), group)
			e.PriceMultiplier = &m
			// The context-length tier selected by the actual usage
			// (phase10-api.md §1).
			e.PriceTier = sell.AppliedTier(e.Usage)
		}
		if e.UpstreamModel != nil {
			if cost, err := g.prices.Lookup(ctx, pricing.KindCost, *e.UpstreamModel, e.ChannelID, st.start); err == nil && cost != nil {
				// Cost prices follow their own schedule but never the group.
				e.CostPriceID = &cost.ID
				e.Cost, _ = cost.Compute(e.Usage, st.start, pricing.One)
			}
		}
	} else {
		// ADR-0006: failed requests (including failed retries) are never charged.
		e.Charge = 0
	}
	if st.subscription != nil {
		// Covered by a plan: count against its quotas instead of the wallet.
		e.QuotaCharge, e.Charge = e.Charge, 0
		var quota *subscription.RecordInput
		if succeeded && st.auth != nil && g.quota != nil {
			quota = &subscription.RecordInput{SubscriptionID: *st.subscription, RequestID: e.RequestID, Model: served, Usage: e.Usage,
				Charge: e.QuotaCharge, At: st.start, Billing: st.billingCtx(served)} // custom.go
		}
		// A fallback may have moved the request from the wallet to a plan:
		// the hold is released.
		g.settle(st, 0, st.reserved, quota)
		g.logs.Add(e)
		return
	}
	g.settle(st, e.Charge, e.SellPriceID != nil || st.reserved, nil)
	g.logs.Add(e)
}

// settle finalizes a request in the background: the wallet (when wallet is
// set: a charge or a hold to release) and the usage counters of the request
// limits in one transaction (phase8-api.md §2.2), then the plan quota usage
// (quota, when set). Failures are retried and finally journaled for replay
// (settlement.go, ADR-0010).
func (g *Gateway) settle(st *reqState, charge money.Amount, wallet bool, quota *subscription.RecordInput) {
	if st.auth == nil {
		return
	}
	var rec *limits.Recorder
	if g.opts.Usage != nil && st.group != nil {
		// rpd counts requests that passed the request limits and reached an
		// upstream channel.
		rec = g.opts.Usage.Recorder(limits.Usage{UserID: st.auth.UserID, Key: st.auth.Key, Group: st.group, At: st.start,
			Counted: st.counted && len(st.entry.Attempts) > 0, Charge: charge})
	}
	if !wallet && rec == nil && quota == nil {
		return
	}
	s := &Settlement{RequestID: st.entry.RequestID, UserID: st.auth.UserID, Wallet: wallet, Charge: charge, Quota: quota}
	if rec != nil {
		s.Increments = rec.Increments()
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.runSettlement(s, rec)
	}()
}

// checkRequestLimits applies the user group's rpm and rpd (every tier).
func (g *Gateway) checkRequestLimits(r *http.Request, st *reqState) *protocol.GatewayError {
	if g.opts.Usage == nil || st.group == nil {
		return nil
	}
	d, err := g.opts.Usage.CheckRequest(r.Context(), st.auth.UserID, st.group, st.start)
	if err != nil {
		g.log.ErrorContext(r.Context(), "request limit check failed", "err", err)
		return protocol.NewError(protocol.ErrInternal, "internal error")
	}
	if d != nil {
		return denial(d)
	}
	st.counted = true
	return nil
}

// checkSpend applies the spend limits before the first platform attempt of a
// wallet-paid request and returns the smallest remaining amount (nil = none).
func (g *Gateway) checkSpend(r *http.Request, st *reqState) (*money.Amount, *protocol.GatewayError) {
	if g.opts.Usage == nil || st.group == nil {
		return nil, nil
	}
	remaining, d, err := g.opts.Usage.CheckSpend(r.Context(), st.auth.UserID, st.auth.Key, st.group, st.start)
	if err != nil {
		g.log.ErrorContext(r.Context(), "spend limit check failed", "err", err)
		return nil, protocol.NewError(protocol.ErrInternal, "internal error")
	}
	if d != nil {
		return nil, denial(d)
	}
	return remaining, nil
}

func denial(d *limits.Denial) *protocol.GatewayError {
	class := protocol.ErrRateLimited
	switch d.Code {
	case limits.CodeRequestLimit:
		class = protocol.ErrUserRequestLimit
	case limits.CodeSpendLimit:
		class = protocol.ErrSpendLimit
	}
	e := protocol.NewError(class, d.Message)
	e.RetryAfter = d.RetryAfter
	return e
}

// CollapseDuplicateV1 rewrites "/v1/v1/…" to "/v1/…". Anthropic SDK clients
// append "/v1/messages" to their base URL, so users who configure the OpenAI
// style base URL ("…/v1") end up requesting "/v1/v1/messages"; accept both.
func CollapseDuplicateV1(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/v1/") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1")
			if r.URL.RawPath != "" {
				r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, "/v1")
			}
		}
		next.ServeHTTP(w, r)
	})
}
