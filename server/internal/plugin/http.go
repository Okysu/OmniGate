package plugin

import (
	"io"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/plugins/sdk.d.ts", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, SDKTypes)
	})
	r.Route("/plugins", func(r chi.Router) {
		r.Use(auth.Require(authz.PluginsRead))
		r.Get("/", h.list)
		r.With(auth.Require(authz.PluginsManage)).Post("/", h.create)
		r.With(auth.Require(authz.PluginsManage)).Post("/import", h.importZip)
		r.Get("/{id}", h.get)
		r.With(auth.Require(authz.PluginsManage)).Patch("/{id}", h.patch)
		r.With(auth.Require(authz.PluginsManage)).Delete("/{id}", h.delete)
		r.Group(func(r chi.Router) {
			r.Use(auth.Require(authz.PluginsManage))
			r.Get("/{id}/draft", h.getDraft)
			r.Put("/{id}/draft", h.putDraft)
			r.Post("/{id}/draft/build", h.build)
			r.Post("/{id}/draft/test", h.test)
			r.Post("/{id}/draft/publish", h.publish)
		})
		r.Get("/{id}/versions/{vid}", h.version)
		r.Get("/{id}/versions/{vid}/export", h.export)
		r.With(auth.Require(authz.PluginsTrust)).Post("/{id}/versions/{vid}/approve", h.approve)
	})
}

func meta(r *http.Request) Meta {
	return Meta{IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), RequestID: httpx.RequestID(r.Context())}
}

func ids(w http.ResponseWriter, r *http.Request, names ...string) ([]uuid.UUID, bool) {
	out := make([]uuid.UUID, len(names))
	for i, n := range names {
		id, err := uuid.Parse(chi.URLParam(r, n))
		if err != nil {
			httpx.WriteError(w, r, apperr.NotFound("插件"))
			return nil, false
		}
		out[i] = id
	}
	return out, true
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := ids(w, r, "id")
	if !ok {
		return
	}
	p, err := h.svc.Get(r.Context(), id[0])
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Template string `json:"template"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Template == "" {
		body.Template = "openai-compatible"
	}
	p, err := h.svc.CreateFromTemplate(r.Context(), auth.PrincipalFrom(r.Context()), body.ID, body.Name, body.Template, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, p)
}

func (h *Handler) importZip(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		httpx.WriteError(w, r, apperr.Validation("请以 multipart/form-data 上传 file 字段（不超过 4 MiB）", nil))
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, apperr.Validation("缺少 file 字段", nil))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		httpx.WriteError(w, r, apperr.Validation("读取上传文件失败", nil))
		return
	}
	p, build, err := h.svc.Import(r.Context(), auth.PrincipalFrom(r.Context()), data, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	d, _ := h.svc.GetDraft(r.Context(), p.ID)
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"plugin": p, "draft": d, "build": build})
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	id, ok := ids(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Status  string `json:"status"`
		Version *int   `json:"version"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Version == nil {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", map[string]any{"version": "必填"}))
		return
	}
	p, err := h.svc.UpdateStatus(r.Context(), auth.PrincipalFrom(r.Context()), id[0], body.Status, *body.Version, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := ids(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), auth.PrincipalFrom(r.Context()), id[0], meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := ids(w, r, "id")
	if !ok {
		return
	}
	d, err := h.svc.GetDraft(r.Context(), id[0])
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

func (h *Handler) putDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := ids(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Files   map[string]string `json:"files"`
		Version *int              `json:"version"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Version == nil || body.Files == nil {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", map[string]any{"version": "必填", "files": "必填"}))
		return
	}
	d, err := h.svc.SaveDraft(r.Context(), auth.PrincipalFrom(r.Context()), id[0], body.Files, *body.Version, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

func (h *Handler) build(w http.ResponseWriter, r *http.Request) {
	id, ok := ids(w, r, "id")
	if !ok {
		return
	}
	b, err := h.svc.BuildDraft(r.Context(), id[0])
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, b)
}

func (h *Handler) test(w http.ResponseWriter, r *http.Request) {
	id, ok := ids(w, r, "id")
	if !ok {
		return
	}
	var tc TestCase
	if err := httpx.DecodeJSON(r, &tc); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	d, err := h.svc.GetDraft(r.Context(), id[0])
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.RunTest(r.Context(), d.Files, tc)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	id, ok := ids(w, r, "id")
	if !ok {
		return
	}
	v, err := h.svc.Publish(r.Context(), auth.PrincipalFrom(r.Context()), id[0], meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, v)
}

func (h *Handler) version(w http.ResponseWriter, r *http.Request) {
	x, ok := ids(w, r, "id", "vid")
	if !ok {
		return
	}
	v, err := h.svc.GetVersion(r.Context(), x[0], x[1])
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	x, ok := ids(w, r, "id", "vid")
	if !ok {
		return
	}
	name, data, err := h.svc.Export(r.Context(), x[0], x[1])
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	_, _ = w.Write(data)
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	x, ok := ids(w, r, "id", "vid")
	if !ok {
		return
	}
	var body struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Decision != "approve" && body.Decision != "reject" {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", map[string]any{"decision": "只能是 approve 或 reject"}))
		return
	}
	v, err := h.svc.Approve(r.Context(), auth.PrincipalFrom(r.Context()), x[0], x[1], body.Decision == "approve", body.Note, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}
