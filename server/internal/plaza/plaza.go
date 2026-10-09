package plaza

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/channel"
	"omnigate/internal/money"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/pricing"
	"omnigate/internal/protocol"
	"omnigate/internal/subscription"
	"omnigate/internal/usergroup"
)

// Channels is the channel registry snapshot (channel.Registry).
type Channels interface {
	Active() []*channel.Runtime
}

// Prices looks up the current sell price (pricing.Service).
type Prices interface {
	Lookup(ctx context.Context, kind, model string, channelID *uuid.UUID, at time.Time) (*pricing.Price, error)
}

// Plans supplies plans and subscriptions (subscription.Service).
type Plans interface {
	ActivePlans(ctx context.Context) ([]subscription.PlanCoverage, error)
	LiveSubscriptions(ctx context.Context, userID uuid.UUID, now time.Time) ([]*subscription.Subscription, error)
}

// Settings tells whether anonymous visitors may open the plaza (settings.Service).
type Settings interface {
	PublicModelPlaza(ctx context.Context) bool
}

// Groups resolves a user's group (usergroup.Service).
type Groups interface {
	ForUser(ctx context.Context, userID uuid.UUID) (*usergroup.Group, error)
}

// Handler serves /api/plaza/* and /api/admin/model-info.
type Handler struct {
	// Groups applies the user's price multiplier to /plaza/mine (nil = ×1).
	Groups Groups

	info     *InfoService
	channels Channels
	prices   Prices
	plans    Plans
	settings Settings
	// currency is rendered as-is (the /api/system/info currency object).
	currency any
}

func NewHandler(info *InfoService, channels Channels, prices Prices, plans Plans, settings Settings, currency any) *Handler {
	return &Handler{info: info, channels: channels, prices: prices, plans: plans, settings: settings, currency: currency}
}

// PublicRoutes mounts GET /plaza/models; the router must run
// auth.Handler.OptionalSession (the endpoint may be anonymous).
func (h *Handler) PublicRoutes(r chi.Router) { r.Get("/plaza/models", h.platformModels) }

// Routes mounts GET /plaza/mine (signed-in users).
func (h *Handler) Routes(r chi.Router) { r.Get("/plaza/mine", h.myModels) }

// AdminRoutes mounts /model-info under /api/admin.
func (h *Handler) AdminRoutes(r chi.Router) {
	m := r.With(auth.Require(authz.ModelsManage))
	m.Get("/model-info", h.listInfo)
	m.Put("/model-info/{model}", h.putInfo)
	m.Delete("/model-info/{model}", h.deleteInfo)
}

// ---- model info ----

func actor(r *http.Request) Actor {
	p := auth.PrincipalFrom(r.Context())
	return Actor{ID: p.UserID, Name: p.Name}
}

func meta(r *http.Request) RequestMeta {
	return RequestMeta{IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), RequestID: httpx.RequestID(r.Context())}
}

// modelParam returns the URL-decoded {model} segment (model names may
// contain '/', sent as %2F; chi matches on the escaped path then).
func modelParam(r *http.Request) (string, error) {
	v := chi.URLParam(r, "model")
	if r.URL.RawPath == "" {
		return v, nil
	}
	return url.PathUnescape(v)
}

func (h *Handler) listInfo(w http.ResponseWriter, r *http.Request) {
	items, err := h.info.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) putInfo(w http.ResponseWriter, r *http.Request) {
	model, err := modelParam(r)
	if err != nil {
		httpx.WriteError(w, r, apperr.Validation("模型资料校验失败", map[string]any{"model": "模型名编码无效"}))
		return
	}
	var in Input
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	info, created, err := h.info.Put(r.Context(), actor(r), model, in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, info)
}

func (h *Handler) deleteInfo(w http.ResponseWriter, r *http.Request) {
	model, err := modelParam(r)
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("模型资料"))
		return
	}
	if err := h.info.Delete(r.Context(), actor(r), model, meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- plaza ----

// Price is the current sell price per million tokens (settlement currency).
// PerImage / ImageInputPerM (phase7-api.md §4.7) and the audio prices
// AudioInputPerM / AudioOutputPerM / PerMinute / PerMCharacters
// (phase9-api.md §1.1) are null when not set.
// Schedule / ScheduleTimezone are the version's time-of-day schedule and
// CurrentMultiplier its multiplier right now (phase8-api.md §3); the amounts
// are the unscheduled unit prices.
type Price struct {
	InputPerM         string                 `json:"inputPerM"`
	OutputPerM        string                 `json:"outputPerM"`
	CacheReadPerM     *string                `json:"cacheReadPerM"`
	CacheWritePerM    *string                `json:"cacheWritePerM"`
	PerRequest        *string                `json:"perRequest"` // fixed fee per request (per-call pricing); null when not set
	PerImage          *string                `json:"perImage"`
	ImageInputPerM    *string                `json:"imageInputPerM"`
	AudioInputPerM    *string                `json:"audioInputPerM"`
	AudioOutputPerM   *string                `json:"audioOutputPerM"`
	PerMinute         *string                `json:"perMinute"`
	PerMCharacters    *string                `json:"perMCharacters"`
	Schedule          []pricing.ScheduleSlot `json:"schedule"`
	ScheduleTimezone  string                 `json:"scheduleTimezone"`
	CurrentMultiplier string                 `json:"currentMultiplier"`
	// Tiers are the context-length tiers with inheritance resolved
	// (phase10-api.md §1.3); null = none.
	Tiers []PriceTier `json:"tiers"`
}

// PriceTier is a context-length tier of a plaza price: the effective unit
// prices of requests whose prompt tokens exceed AboveInputTokens. Like the
// base fields, cacheReadPerM / cacheWritePerM are null when 0 and the image /
// audio token prices null when billed at this tier's input / output price.
type PriceTier struct {
	AboveInputTokens int64   `json:"aboveInputTokens"`
	InputPerM        string  `json:"inputPerM"`
	OutputPerM       string  `json:"outputPerM"`
	CacheReadPerM    *string `json:"cacheReadPerM"`
	CacheWritePerM   *string `json:"cacheWritePerM"`
	ImageInputPerM   *string `json:"imageInputPerM"`
	AudioInputPerM   *string `json:"audioInputPerM"`
	AudioOutputPerM  *string `json:"audioOutputPerM"`
}

// priceView renders p with every unit price multiplied by m.
func priceView(p *pricing.Price, m pricing.Multiplier, now time.Time) *Price {
	f := func(a money.Amount) string {
		v, err := m.Apply(a)
		if err != nil {
			v = a
		}
		return v.String()
	}
	out := &Price{InputPerM: f(p.InputPerM), OutputPerM: f(p.OutputPerM),
		CacheReadPerM: amountPtr(f(p.CacheReadPM)), CacheWritePerM: amountPtr(f(p.CacheWritePM)),
		PerRequest: amountPtr(f(p.PerRequest)), PerImage: amountPtr(f(p.PerImage)), Schedule: p.Schedule, ScheduleTimezone: p.ScheduleTimezone,
		CurrentMultiplier: p.ScheduleMultiplier(now).String()}
	optional := func(a *money.Amount) *string {
		if a == nil {
			return nil
		}
		v := f(*a)
		return &v
	}
	out.ImageInputPerM = optional(p.ImageInputPM)
	out.AudioInputPerM, out.AudioOutputPerM = optional(p.AudioInputPM), optional(p.AudioOutputPM)
	out.PerMinute, out.PerMCharacters = amountPtr(f(p.PerMinute)), amountPtr(f(p.PerMCharacters))
	for i := range p.Tiers {
		t := p.WithTier(&p.Tiers[i])
		out.Tiers = append(out.Tiers, PriceTier{AboveInputTokens: p.Tiers[i].AboveInputTokens,
			InputPerM: f(t.InputPerM), OutputPerM: f(t.OutputPerM),
			CacheReadPerM: amountPtr(f(t.CacheReadPM)), CacheWritePerM: amountPtr(f(t.CacheWritePM)),
			ImageInputPerM: optional(t.ImageInputPM), AudioInputPerM: optional(t.AudioInputPM),
			AudioOutputPerM: optional(t.AudioOutputPM)})
	}
	return out
}

// PlanRef names a plan covering a model.
type PlanRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Model is one plaza entry (PlazaModel in the contract).
type Model struct {
	Model         string       `json:"model"`
	DisplayName   string       `json:"displayName"`
	Description   string       `json:"description"`
	Vendor        string       `json:"vendor"`
	Tags          []string     `json:"tags"`
	ContextWindow *int64       `json:"contextWindow"`
	MaxOutput     *int64       `json:"maxOutput"`
	Capabilities  Capabilities `json:"capabilities"`
	Protocols     []string     `json:"protocols"`
	Price         *Price       `json:"price"`
	Plans         []PlanRef    `json:"plans"`

	sortOrder int
}

// Sources counts the user's usable channels per tier.
type Sources struct {
	Own      int `json:"own"`
	Shared   int `json:"shared"`
	Platform int `json:"platform"`
}

// SubscriptionRef is the subscription covering a model.
type SubscriptionRef struct {
	ID       uuid.UUID `json:"id"`
	PlanName string    `json:"planName"`
}

// MyModel is one entry of GET /api/plaza/mine. Price is multiplied by the
// user's group multiplier; BasePrice is the list price (phase8-api.md §1.1).
type MyModel struct {
	Model
	BasePrice       *Price           `json:"basePrice"`
	PriceMultiplier string           `json:"priceMultiplier"`
	Sources         Sources          `json:"sources"`
	Billing         string           `json:"billing"` // free | platform
	Subscription    *SubscriptionRef `json:"subscription"`
}

// served aggregates the channels serving one model.
type served struct {
	openai  bool // an OpenAI-compatible channel serves it (embeddings)
	sources Sources
}

// clientProtocols lists the client protocols a model can be called with,
// driven by the model info capabilities:
//   - chat models (tools / vision / reasoning, or no capability marked at
//     all) get Chat, Responses and Messages — the gateway converts between
//     them;
//   - embeddings, image generation and audio endpoints are listed only when
//     the model is marked with that capability (and an OpenAI-compatible
//     channel serves it). A pure image or embedding model therefore shows
//     only its own endpoint.
func clientProtocols(openai bool, caps Capabilities) []string {
	special := caps.Embedding || caps.ImageGeneration || caps.AudioInput || caps.AudioOutput
	out := []string{}
	if caps.Tools || caps.Vision || caps.Reasoning || !special {
		out = append(out, protocol.OpenAIChat, protocol.OpenAIResponses, protocol.Anthropic)
	}
	if openai {
		if caps.Embedding {
			out = append(out, protocol.OpenAIEmbeddings)
		}
		if caps.ImageGeneration {
			out = append(out, protocol.OpenAIImages)
		}
		if caps.AudioInput || caps.AudioOutput {
			out = append(out, protocol.OpenAIAudio)
		}
	}
	return out
}

func amountPtr(s string) *string {
	if s == "0" {
		return nil
	}
	return &s
}

// catalog holds what every plaza entry needs besides its channels.
type catalog struct {
	info  map[string]*Info
	plans []subscription.PlanCoverage
	now   time.Time
	// mult is the viewer's group multiplier (One for /plaza/models).
	mult pricing.Multiplier
}

func (h *Handler) loadCatalog(ctx context.Context) (*catalog, error) {
	info, err := h.info.All(ctx)
	if err != nil {
		return nil, err
	}
	plans, err := h.plans.ActivePlans(ctx)
	if err != nil {
		return nil, err
	}
	return &catalog{info: info, plans: plans, now: time.Now().UTC(), mult: pricing.One}, nil
}

func (h *Handler) entry(ctx context.Context, c *catalog, model string, s *served) (Model, *Price, error) {
	m := Model{Model: model, Tags: []string{}, Plans: []PlanRef{}}
	var base *Price
	if i := c.info[model]; i != nil {
		m.DisplayName, m.Description, m.Vendor, m.Tags = i.DisplayName, i.Description, i.Vendor, i.Tags
		m.ContextWindow, m.MaxOutput, m.Capabilities, m.sortOrder = i.ContextWindow, i.MaxOutput, i.Capabilities, i.SortOrder
	}
	m.Protocols = clientProtocols(s.openai, m.Capabilities)
	p, err := h.prices.Lookup(ctx, pricing.KindSell, model, nil, c.now)
	if err != nil {
		return m, nil, err
	}
	if p != nil {
		base = priceView(p, pricing.One, c.now)
		m.Price = base
		if c.mult != pricing.One {
			m.Price = priceView(p, c.mult, c.now)
		}
	}
	for _, pl := range c.plans {
		if pl.Covers(model) {
			m.Plans = append(m.Plans, PlanRef{ID: pl.ID, Name: pl.Name})
		}
	}
	return m, base, nil
}

func sortModels[T any](items []T, key func(T) *Model) {
	slices.SortFunc(items, func(a, b T) int {
		x, y := key(a), key(b)
		if x.sortOrder != y.sortOrder {
			return x.sortOrder - y.sortOrder
		}
		return strings.Compare(x.Model, y.Model)
	})
}

// platformModels serves GET /api/plaza/models: models served by global
// platform channels, without hidden ones. Channel names, counts and cost
// prices are never exposed.
func (h *Handler) platformModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if auth.PrincipalFrom(ctx) == nil && !h.settings.PublicModelPlaza(ctx) {
		httpx.WriteError(w, r, apperr.Unauthenticated())
		return
	}
	byModel := map[string]*served{}
	for _, rt := range h.channels.Active() {
		if !rt.PlatformOwned() || rt.Scope != authz.ScopeGlobal {
			continue
		}
		for _, m := range rt.ModelNames() {
			s := byModel[m]
			if s == nil {
				s = &served{}
				byModel[m] = s
			}
			s.openai = s.openai || rt.Type == channel.TypeOpenAI
		}
	}
	c, err := h.loadCatalog(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items := []Model{}
	for model, s := range byModel {
		if i := c.info[model]; i != nil && i.Hidden {
			continue
		}
		m, _, err := h.entry(ctx, c, model, s)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		items = append(items, m)
	}
	sortModels(items, func(m Model) *Model { return &m })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "currency": h.currency})
}

// myModels serves GET /api/plaza/mine: every model the user can call (own,
// shared and platform channels, hidden models included).
func (h *Handler) myModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.PrincipalFrom(ctx).UserID
	byModel := map[string]*served{}
	for _, rt := range h.channels.Active() {
		if !rt.UsableBy(user) {
			continue
		}
		tier := rt.TierFor(user)
		for _, m := range rt.ModelNames() {
			s := byModel[m]
			if s == nil {
				s = &served{}
				byModel[m] = s
			}
			s.openai = s.openai || rt.Type == channel.TypeOpenAI
			switch tier {
			case channel.TierOwn:
				s.sources.Own++
			case channel.TierShared:
				s.sources.Shared++
			default:
				s.sources.Platform++
			}
		}
	}
	c, err := h.loadCatalog(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	subs, err := h.plans.LiveSubscriptions(ctx, user, c.now)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if h.Groups != nil {
		g, err := h.Groups.ForUser(ctx, user)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		c.mult = g.Multiplier
	}
	items := []MyModel{}
	for model, s := range byModel {
		m, base, err := h.entry(ctx, c, model, s)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		item := MyModel{Model: m, BasePrice: base, PriceMultiplier: c.mult.String(), Sources: s.sources, Billing: "platform"}
		if s.sources.Own+s.sources.Shared > 0 {
			item.Billing = "free"
		}
		for _, sub := range subs { // expiring first first
			if sub.Covers(model) {
				item.Subscription = &SubscriptionRef{ID: sub.ID, PlanName: sub.PlanName}
				break
			}
		}
		items = append(items, item)
	}
	sortModels(items, func(m MyModel) *Model { return &m.Model })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "currency": h.currency})
}
