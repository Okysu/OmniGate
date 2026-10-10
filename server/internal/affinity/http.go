package affinity

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/httpx"
)

// ActionClear is the audit action of clearing session bindings.
const ActionClear = "affinity.clear"

// Handler serves /api/admin/affinity (the setting itself is gateway.affinity
// in /api/admin/settings).
type Handler struct {
	svc *Service
	rec *audit.Recorder
}

func NewHandler(svc *Service, rec *audit.Recorder) *Handler { return &Handler{svc: svc, rec: rec} }

// AdminRoutes registers the endpoints; mount on the /api/admin router.
func (h *Handler) AdminRoutes(r chi.Router) {
	r.With(auth.Require(authz.SettingsRead)).Get("/affinity/stats", h.stats)
	r.With(auth.Require(authz.SettingsWrite)).Post("/affinity/clear", h.clear)
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, h.svc.Stats(r.Context()))
}

func (h *Handler) clear(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// Rule limits the clear to one rule's bindings ("" / absent = all).
		Rule *string `json:"rule"`
	}
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(r, &body); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	rule := ""
	if body.Rule != nil {
		rule = strings.TrimSpace(*body.Rule)
		if len([]rune(rule)) > maxNameLen {
			httpx.WriteError(w, r, apperr.Validation("参数校验失败", map[string]any{"rule": "规则名称过长"}))
			return
		}
	}
	n := h.svc.Clear(rule)
	p := auth.PrincipalFrom(r.Context())
	md := map[string]any{"cleared": n}
	if rule != "" {
		md["rule"] = rule
	}
	rid := "gateway.affinity"
	if err := h.rec.Record(r.Context(), nil, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: ActionClear, ResourceType: "system_settings",
		ResourceID: &rid, IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), RequestID: httpx.RequestID(r.Context()), Metadata: md}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"cleared": n, "stats": h.svc.Stats(r.Context())})
}
