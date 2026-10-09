package settings

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/httpx"
)

// Handler serves /api/admin/settings.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// AdminRoutes registers the endpoints; mount on the /api/admin router.
func (h *Handler) AdminRoutes(r chi.Router) {
	m := r.With(auth.Require(authz.SettingsWrite))
	m.Get("/settings", h.get)
	m.Patch("/settings", h.patch)
}

type view struct {
	Settings Settings          `json:"settings"`
	Sources  map[string]string `json:"sources"`
	Readonly map[string]any    `json:"readonly"`
	Version  int               `json:"version"`
}

func (h *Handler) view(s *Snapshot) view {
	ro := make(map[string]any, len(h.svc.Readonly())+1)
	for k, v := range h.svc.Readonly() {
		ro[k] = v
	}
	// Effective SMTP state (database or environment), for the settings page.
	ro["smtpConfigured"] = s.values.smtp().Configured()
	return view{Settings: s.Settings, Sources: s.Sources, Readonly: ro, Version: s.Version}
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	snap, err := h.svc.Get(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.view(snap))
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version  *int  `json:"version"`
		Settings Patch `json:"settings"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Version == nil {
		httpx.WriteError(w, r, apperr.Validation("设置校验失败", map[string]any{"version": "必填"}))
		return
	}
	p := auth.PrincipalFrom(r.Context())
	snap, err := h.svc.Update(r.Context(), Actor{ID: p.UserID, Name: p.Name}, *body.Version, body.Settings,
		RequestMeta{IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), RequestID: httpx.RequestID(r.Context())})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.view(snap))
}
