package routing

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/protocol"
)

// RuleRef names the rule a preview matched.
type RuleRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// PreviewCandidate is one channel in attempt order.
type PreviewCandidate struct {
	ChannelID       uuid.UUID `json:"channelId"`
	ChannelName     string    `json:"channelName"`
	ChannelType     string    `json:"channelType"`
	Tier            string    `json:"tier"` // own | shared | platform (relative to the previewed user)
	Priority        int       `json:"priority"`
	Weight          int       `json:"weight"`
	UpstreamDialect string    `json:"upstreamDialect"`
	ConversionHops  int       `json:"conversionHops"`
	Breaker         string    `json:"breaker"` // closed | open | half_open
	LatencyMs       *int64    `json:"latencyMs"`
	CostPerM        *string   `json:"costPerM"`
	Skipped         *string   `json:"skipped"`
}

// Preview is the response of POST /api/admin/routes/preview.
type Preview struct {
	Rule           *RuleRef           `json:"rule"`
	Strategy       string             `json:"strategy"`
	MaxAttempts    int                `json:"maxAttempts"`
	Candidates     []PreviewCandidate `json:"candidates"`
	FallbackModels []string           `json:"fallbackModels"`
	// TierOrder is the order in which tiers are tried (own → shared → platform);
	// the rule only applies to the platform tier.
	TierOrder []string `json:"tierOrder"`
}

// Previewer computes a preview (implemented by the gateway, which owns the
// candidate selection logic).
type Previewer interface {
	Preview(ctx context.Context, userID uuid.UUID, role identity.Role, model, inbound string) (*Preview, error)
}

// UserLookup resolves the role of the user a preview is computed for.
type UserLookup interface {
	Get(ctx context.Context, id uuid.UUID) (*identity.User, error)
}

// Handler serves /api/admin/routes.
type Handler struct {
	svc     *Service
	users   UserLookup
	preview Previewer
}

func NewHandler(svc *Service, users UserLookup, preview Previewer) *Handler {
	return &Handler{svc: svc, users: users, preview: preview}
}

// AdminRoutes registers the endpoints; mount on the /api/admin router.
func (h *Handler) AdminRoutes(r chi.Router) {
	m := r.With(auth.Require(authz.RoutesManage))
	m.Get("/routes", h.list)
	m.Post("/routes", h.create)
	m.Put("/routes/order", h.reorder)
	m.Post("/routes/preview", h.previewRoute)
	m.Patch("/routes/{id}", h.patch)
	m.Delete("/routes/{id}", h.remove)
}

func meta(r *http.Request) RequestMeta {
	return RequestMeta{IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), RequestID: httpx.RequestID(r.Context())}
}

func actor(r *http.Request) Actor {
	p := auth.PrincipalFrom(r.Context())
	return Actor{ID: p.UserID, Name: p.Name}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	rules, err := h.svc.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": rules})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in.Version != nil {
		httpx.WriteError(w, r, invalid(map[string]any{"version": "创建时不能指定"}))
		return
	}
	rule, err := h.svc.Create(r.Context(), actor(r), in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, rule)
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("路由规则"))
		return
	}
	var in Input
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rule, err := h.svc.Update(r.Context(), actor(r), id, in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rule)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("路由规则"))
		return
	}
	if err := h.svc.Delete(r.Context(), actor(r), id, meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) reorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []uuid.UUID `json:"ids"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.IDs == nil {
		httpx.WriteError(w, r, invalid(map[string]any{"ids": "必填"}))
		return
	}
	rules, err := h.svc.Reorder(r.Context(), actor(r), body.IDs, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": rules})
}

var inbounds = []string{protocol.OpenAIChat, protocol.OpenAIResponses, protocol.Anthropic, protocol.OpenAIEmbeddings, protocol.OpenAIImagesGenerations,
	protocol.OpenAIAudioTranscriptions, protocol.OpenAIAudioSpeech}

func (h *Handler) previewRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model   string     `json:"model"`
		UserID  *uuid.UUID `json:"userId"`
		Inbound string     `json:"inbound"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	if body.Model == "" {
		details["model"] = "必填"
	}
	if !slices.Contains(inbounds, body.Inbound) {
		details["inbound"] = "只能是 " + strings.Join(inbounds, "、")
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	p := auth.PrincipalFrom(r.Context())
	userID, role := p.UserID, p.Role
	if body.UserID != nil && *body.UserID != p.UserID {
		u, err := h.users.Get(r.Context(), *body.UserID)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		userID, role = u.ID, u.Role
	}
	out, err := h.preview.Preview(r.Context(), userID, role, body.Model, body.Inbound)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
