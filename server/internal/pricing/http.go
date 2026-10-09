package pricing

import (
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/httpx"
)

// ModelLister reports the logical models usable by a user (channel registry).
type ModelLister interface {
	Models(userID uuid.UUID, allowed []uuid.UUID) map[string]int
	AllModels() map[string]int
}

type Handler struct {
	svc    *Service
	models ModelLister
}

func NewHandler(svc *Service, models ModelLister) *Handler { return &Handler{svc: svc, models: models} }

// Routes mounts /models (any signed-in user).
func (h *Handler) Routes(r chi.Router) { r.Get("/models", h.listModels) }

// AdminRoutes mounts /prices under /api/admin.
func (h *Handler) AdminRoutes(r chi.Router) {
	r.With(auth.Require(authz.ModelsManage)).Get("/prices", h.listPrices)
	r.With(auth.Require(authz.ModelsManage)).Post("/prices", h.createPrice)
}

func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	counts := h.models.Models(p.UserID, nil)
	// ?scope=all (models.manage): every model on any enabled channel, so prices
	// can be set for models on channels the manager cannot use themselves.
	if r.URL.Query().Get("scope") == "all" {
		if !p.Can(authz.ModelsManage) {
			httpx.WriteError(w, r, apperr.Forbidden())
			return
		}
		counts = h.models.AllModels()
	}
	type entry struct {
		Model    string `json:"model"`
		Channels int    `json:"channels"`
		Price    any    `json:"price"`
	}
	out := []entry{}
	now := time.Now().UTC()
	for m, n := range counts {
		pr, err := h.svc.Lookup(r.Context(), KindSell, m, nil, now)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, entry{Model: m, Channels: n, Price: pr.JSON()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) listPrices(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	q := ListQuery{Kind: r.URL.Query().Get("kind"), Model: r.URL.Query().Get("model"), Offset: pg.Offset(), Limit: pg.PageSize}
	if v := r.URL.Query().Get("channelId"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.WriteError(w, r, apperr.Validation("channelId 不是合法的 UUID", nil))
			return
		}
		q.ChannelID = &id
	}
	items, total, err := h.svc.List(r.Context(), q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[any]{Items: items, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

func (h *Handler) createPrice(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.svc.Create(r.Context(), auth.PrincipalFrom(r.Context()), in,
		httpx.IPPrefix(httpx.ClientIP(r.Context())), httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, p.JSON())
}
