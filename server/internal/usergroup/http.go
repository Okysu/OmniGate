package usergroup

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/platform/httpx"
)

// Handler serves /api/admin/groups and PUT /api/admin/users/{id}/group.
type Handler struct {
	svc   *Service
	users *identity.Store
}

func NewHandler(svc *Service, users *identity.Store) *Handler {
	return &Handler{svc: svc, users: users}
}

// AdminRoutes mounts the endpoints under /api/admin.
func (h *Handler) AdminRoutes(r chi.Router) {
	r.With(auth.Require(authz.UsersRead)).Get("/groups", h.list)
	r.With(auth.Require(authz.UsersWrite)).Post("/groups", h.create)
	r.With(auth.Require(authz.UsersWrite)).Patch("/groups/{id}", h.update)
	r.With(auth.Require(authz.UsersWrite)).Delete("/groups/{id}", h.delete)
	r.With(auth.Require(authz.UsersWrite)).Put("/users/{id}/group", h.setUserGroup)
}

// MetaOf returns the audit context of r.
func MetaOf(r *http.Request) Meta {
	return Meta{IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), RequestID: httpx.RequestID(r.Context())}
}

func pathID(w http.ResponseWriter, r *http.Request, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound(what))
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	g, err := h.svc.Create(r.Context(), auth.PrincipalFrom(r.Context()), in, MetaOf(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, g)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "用户组")
	if !ok {
		return
	}
	var in Input
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	g, err := h.svc.Update(r.Context(), auth.PrincipalFrom(r.Context()), id, in, MetaOf(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, g)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "用户组")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), auth.PrincipalFrom(r.Context()), id, MetaOf(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setGroupBody struct {
	GroupID *string `json:"groupId"`
}

// ParseGroupID validates a groupId field.
func ParseGroupID(raw *string) (uuid.UUID, error) {
	if raw == nil || *raw == "" {
		return uuid.Nil, apperr.Validation("参数校验失败", map[string]any{"groupId": "必填"})
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		return uuid.Nil, apperr.Validation("参数校验失败", map[string]any{"groupId": "用户组不存在"})
	}
	return id, nil
}

// setUserGroup serves PUT /users/{id}/group: {groupId} → the updated user.
func (h *Handler) setUserGroup(w http.ResponseWriter, r *http.Request) {
	uid, ok := pathID(w, r, "用户")
	if !ok {
		return
	}
	var body setGroupBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	gid, err := ParseGroupID(body.GroupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err := h.svc.SetUserGroup(r.Context(), auth.PrincipalFrom(r.Context()), uid, gid, MetaOf(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	u, err := h.users.Get(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}
