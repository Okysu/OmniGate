package subscription

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/money"
	"omnigate/internal/platform/httpx"
)

// Handler serves /api/plans and /api/billing/{subscriptions,reset-cards}
// (Routes) and /api/admin/billing/{plans,subscriptions,reset-cards}
// (AdminRoutes).
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers the user endpoints; mount on the /api router (session required).
func (h *Handler) Routes(r chi.Router) {
	r.Get("/plans", h.catalog)
	r.With(auth.Require(authz.BillingOwn)).Get("/billing/subscriptions", h.mine)
	r.With(auth.Require(authz.BillingOwn)).Get("/billing/preferences", h.getPrefs)
	r.With(auth.Require(authz.BillingOwn)).Put("/billing/preferences", h.putPrefs)
	h.cardRoutes(r)
}

func (h *Handler) getPrefs(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.QuotaOverflow(r.Context(), auth.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, Preferences{QuotaOverflow: v})
}

func (h *Handler) putPrefs(w http.ResponseWriter, r *http.Request) {
	var body Preferences
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.svc.SetPreferences(r.Context(), actor(r), body, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

// AdminRoutes registers the management endpoints; mount on the /api/admin router.
func (h *Handler) AdminRoutes(r chi.Router) {
	m := r.With(auth.Require(authz.BillingManage))
	m.Get("/billing/plans", h.listPlans)
	m.Get("/billing/meters", h.meters)
	m.Post("/billing/plans", h.createPlan)
	m.Patch("/billing/plans/{id}", h.patchPlan)
	m.Get("/billing/subscriptions", h.listSubs)
	m.Post("/billing/subscriptions", h.grant)
	m.Post("/billing/subscriptions/{id}/cancel", h.cancel)
	m.Post("/billing/subscriptions/reset-quota", h.resetQuota)
	m.Post("/billing/subscriptions/extend", h.extend)
	h.cardAdminRoutes(r)
}

// ---- JSON views ----

// PlanJSON is the API form of a plan (contract §1).
type PlanJSON struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ListPrice   *string   `json:"listPrice"`
	Duration    string    `json:"duration"`
	Models      []string  `json:"models"`
	Rules       []Rule    `json:"rules"`
	Stackable   bool      `json:"stackable"`
	Status      string    `json:"status"`
	Subscribers int       `json:"subscribers"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// PlanView renders p.
func PlanView(p *Plan) PlanJSON {
	var price *string
	if p.ListPrice != nil {
		price = ptr(p.ListPrice.String())
	}
	return PlanJSON{ID: p.ID, Name: p.Name, Description: p.Description, ListPrice: price, Duration: p.Duration,
		Models: p.Models, Rules: p.Rules, Stackable: p.Stackable, Status: p.Status, Subscribers: p.Subscribers,
		Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// CatalogPlanJSON is the user-facing plan catalog entry: what a plan offers,
// without admin-only data (subscriber counts, status, edit version).
type CatalogPlanJSON struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ListPrice   *string   `json:"listPrice"`
	Duration    string    `json:"duration"`
	Models      []string  `json:"models"`
	Rules       []Rule    `json:"rules"`
	Stackable   bool      `json:"stackable"`
}

func catalogViews(ps []*Plan) []CatalogPlanJSON {
	out := make([]CatalogPlanJSON, len(ps))
	for i, p := range ps {
		v := PlanView(p)
		out[i] = CatalogPlanJSON{ID: v.ID, Name: v.Name, Description: v.Description, ListPrice: v.ListPrice,
			Duration: v.Duration, Models: v.Models, Rules: v.Rules, Stackable: v.Stackable}
	}
	return out
}

func planViews(ps []*Plan) []PlanJSON {
	out := make([]PlanJSON, len(ps))
	for i, p := range ps {
		out[i] = PlanView(p)
	}
	return out
}

// ---- helpers ----

func meta(r *http.Request) RequestMeta {
	return RequestMeta{IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), RequestID: httpx.RequestID(r.Context())}
}

func actor(r *http.Request) Actor {
	p := auth.PrincipalFrom(r.Context())
	return Actor{ID: p.UserID, Name: p.Name}
}

func invalid(details map[string]any) error { return apperr.Validation("参数校验失败", details) }

// ---- user endpoints ----

func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()) == nil {
		httpx.WriteError(w, r, apperr.Unauthenticated())
		return
	}
	items, total, err := h.svc.ListPlans(r.Context(), PlanActive, 0, 1000)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[CatalogPlanJSON]{Items: catalogViews(items), Total: total, Page: 1, PageSize: len(items)})
}

func (h *Handler) mine(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListMine(r.Context(), auth.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[*SubscriptionView]{Items: items, Total: len(items), Page: 1, PageSize: mySubsLimit})
}

// ---- admin: plans ----

func (h *Handler) listPlans(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != PlanActive && status != PlanArchived {
		httpx.WriteError(w, r, invalid(map[string]any{"status": "必须为 active 或 archived"}))
		return
	}
	pg := httpx.ParsePage(r)
	items, total, err := h.svc.ListPlans(r.Context(), status, pg.Offset(), pg.PageSize)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[PlanJSON]{Items: planViews(items), Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

type planBody struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	ListPrice   json.RawMessage `json:"listPrice"` // absent: unchanged; null: none
	Duration    *string         `json:"duration"`
	Models      *[]string       `json:"models"`
	Rules       *[]Rule         `json:"rules"`
	Stackable   *bool           `json:"stackable"`
	Status      *string         `json:"status"`
	Version     *int            `json:"version"`
}

// listPrice decodes the tri-state listPrice field.
func (b *planBody) listPrice(details map[string]any) (set bool, price *money.Amount) {
	if b.ListPrice == nil {
		return false, nil
	}
	if bytes.Equal(bytes.TrimSpace(b.ListPrice), []byte("null")) {
		return true, nil
	}
	var s string
	if err := json.Unmarshal(b.ListPrice, &s); err != nil {
		details["listPrice"] = "必须是十进制字符串或 null"
		return true, nil
	}
	a, err := money.Parse(s)
	if err != nil {
		details["listPrice"] = "不是合法的金额（十进制字符串，最多 9 位小数）"
		return true, nil
	}
	return true, &a
}

func (h *Handler) createPlan(w http.ResponseWriter, r *http.Request) {
	var body planBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	in := PlanInput{}
	_, in.ListPrice = body.listPrice(details)
	if body.Name != nil {
		in.Name = *body.Name
	}
	if body.Description != nil {
		in.Description = *body.Description
	}
	if body.Duration != nil {
		in.Duration = *body.Duration
	}
	if body.Models != nil {
		in.Models = *body.Models
	}
	if body.Rules != nil {
		in.Rules = *body.Rules
	}
	if body.Stackable != nil {
		in.Stackable = *body.Stackable
	}
	if body.Status != nil {
		in.Status = *body.Status
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	p, err := h.svc.CreatePlan(r.Context(), actor(r), in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, PlanView(p))
}

func (h *Handler) patchPlan(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("套餐"))
		return
	}
	var body planBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	patch := PlanPatch{Name: body.Name, Description: body.Description, Duration: body.Duration, Models: body.Models,
		Rules: body.Rules, Stackable: body.Stackable, Status: body.Status}
	patch.SetListPrice, patch.ListPrice = body.listPrice(details)
	if body.Version == nil {
		details["version"] = "必填"
	} else {
		patch.Version = *body.Version
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	p, err := h.svc.UpdatePlan(r.Context(), actor(r), id, patch, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, PlanView(p))
}

// ---- admin: subscriptions ----

func parseUUIDParam(r *http.Request, name string, details map[string]any) *uuid.UUID {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		details[name] = "不是合法的 ID"
		return nil
	}
	return &id
}

func (h *Handler) listSubs(w http.ResponseWriter, r *http.Request) {
	details := map[string]any{}
	f := SubscriptionFilter{
		UserID: parseUUIDParam(r, "userId", details),
		PlanID: parseUUIDParam(r, "planId", details),
		Status: r.URL.Query().Get("status"),
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	pg := httpx.ParsePage(r)
	items, total, err := h.svc.ListSubscriptions(r.Context(), f, pg.Offset(), pg.PageSize)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[*SubscriptionView]{Items: items, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

type grantBody struct {
	UserID  *uuid.UUID `json:"userId"`
	PlanID  *uuid.UUID `json:"planId"`
	Periods *int       `json:"periods"`
}

func (h *Handler) grant(w http.ResponseWriter, r *http.Request) {
	var body grantBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	if body.UserID == nil {
		details["userId"] = "必填"
	}
	if body.PlanID == nil {
		details["planId"] = "必填"
	}
	if body.Periods == nil {
		details["periods"] = "必填"
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	v, renewed, err := h.svc.AdminGrant(r.Context(), actor(r), *body.UserID, *body.PlanID, *body.Periods, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusCreated
	if renewed {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, v)
}

type cancelBody struct {
	Note string `json:"note"`
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("订阅"))
		return
	}
	var body cancelBody
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(r, &body); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	v, err := h.svc.Cancel(r.Context(), actor(r), id, body.Note, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

// ---- admin: bulk operations (phase7-api.md §3) ----

type resetBody struct {
	Target          json.RawMessage `json:"target"`
	Rules           json.RawMessage `json:"rules"`
	IncludeLifetime bool            `json:"includeLifetime"`
	Note            string          `json:"note"`
}

type extendBody struct {
	Target   json.RawMessage `json:"target"`
	Duration string          `json:"duration"`
	Note     string          `json:"note"`
}

func isNullJSON(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// parseTarget decodes {ids: string[]} or {planId: string | null, status: "active"}.
func parseTarget(raw json.RawMessage, details map[string]any) Target {
	var obj map[string]json.RawMessage
	if isNullJSON(raw) || json.Unmarshal(raw, &obj) != nil || obj == nil {
		details["target"] = "必填：{ids} 或 {planId, status: 'active'}"
		return Target{}
	}
	if ids, ok := obj["ids"]; ok {
		var list []string
		if json.Unmarshal(ids, &list) != nil {
			details["target"] = "ids 必须是订阅 ID 数组"
			return Target{}
		}
		t := Target{IDs: []uuid.UUID{}}
		seen := map[uuid.UUID]bool{}
		for _, v := range list {
			id, err := uuid.Parse(v)
			if err != nil {
				details["target"] = "ids 包含无效的订阅 ID：" + v
				return Target{}
			}
			if !seen[id] {
				seen[id] = true
				t.IDs = append(t.IDs, id)
			}
		}
		return t
	}
	var status string
	if json.Unmarshal(obj["status"], &status) != nil || status != StatusActive {
		details["target"] = "按套餐选择时 status 必须为 active"
		return Target{}
	}
	t := Target{ByPlan: true}
	if p, ok := obj["planId"]; ok && !isNullJSON(p) {
		var s string
		id, err := uuid.Nil, error(nil)
		if err = json.Unmarshal(p, &s); err == nil {
			id, err = uuid.Parse(s)
		}
		if err != nil {
			details["target"] = "planId 必须是套餐 ID 或 null"
			return Target{}
		}
		t.PlanID = &id
	}
	return t
}

func dryRun(r *http.Request) bool {
	v := r.URL.Query().Get("dryRun")
	return v == "true" || v == "1"
}

func (h *Handler) resetQuota(w http.ResponseWriter, r *http.Request) {
	var body resetBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	in := ResetInput{Target: parseTarget(body.Target, details), IncludeLifetime: body.IncludeLifetime, Note: body.Note}
	if !isNullJSON(body.Rules) {
		if json.Unmarshal(body.Rules, &in.Rules) != nil || in.Rules == nil {
			details["rules"] = "必须是规则 ID 数组或 null"
		}
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	res, err := h.svc.ResetQuota(r.Context(), actor(r), in, dryRun(r), meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) extend(w http.ResponseWriter, r *http.Request) {
	var body extendBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	in := ExtendInput{Target: parseTarget(body.Target, details), Duration: body.Duration, Note: body.Note}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	res, err := h.svc.Extend(r.Context(), actor(r), in, dryRun(r), meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// meters lists the meters quota rules can use: built-in ones and the custom
// meters of enabled billing plugins (phase9-api.md §3).
func (h *Handler) meters(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.Meters(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
