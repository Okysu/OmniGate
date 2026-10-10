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
	"omnigate/internal/platform/httpx"
)

// Reset card endpoints (phase11-api.md §2).

// cardRoutes registers the user endpoints (billing.own).
func (h *Handler) cardRoutes(r chi.Router) {
	m := r.With(auth.Require(authz.BillingOwn))
	m.Get("/billing/reset-cards", h.myCards)
	m.Get("/billing/reset-cards/{id}/preview", h.previewCard)
	m.Post("/billing/reset-cards/{id}/use", h.useCard)
}

// cardAdminRoutes registers the management endpoints (billing.manage).
func (h *Handler) cardAdminRoutes(r chi.Router) {
	m := r.With(auth.Require(authz.BillingManage))
	m.Get("/billing/reset-cards", h.listCards)
	m.Get("/billing/reset-cards/batches", h.listCardBatches)
	m.Post("/billing/reset-cards/batches", h.issueCards)
	m.Post("/billing/reset-cards/batches/{id}/revoke", h.revokeCardBatch)
}

func (h *Handler) myCards(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.ListMyCards(r.Context(), auth.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func cardID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, apperr.NotFound("重置卡")
	}
	return id, nil
}

func (h *Handler) previewCard(w http.ResponseWriter, r *http.Request) {
	id, err := cardID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.svc.PreviewCard(r.Context(), auth.PrincipalFrom(r.Context()).UserID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type useCardBody struct {
	SubscriptionID *uuid.UUID `json:"subscriptionId"`
}

func (h *Handler) useCard(w http.ResponseWriter, r *http.Request) {
	id, err := cardID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body useCardBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.SubscriptionID == nil {
		httpx.WriteError(w, r, invalid(map[string]any{"subscriptionId": "必填"}))
		return
	}
	out, err := h.svc.UseCard(r.Context(), actor(r), id, *body.SubscriptionID, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ---- admin ----

func (h *Handler) listCards(w http.ResponseWriter, r *http.Request) {
	details := map[string]any{}
	f := CardFilter{UserID: parseUUIDParam(r, "userId", details), BatchID: parseUUIDParam(r, "batchId", details)}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	pg := httpx.ParsePage(r)
	items, total, err := h.svc.ListCards(r.Context(), f, pg.Offset(), pg.PageSize)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[*Card]{Items: items, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

func (h *Handler) listCardBatches(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	items, total, err := h.svc.ListCardBatches(r.Context(), pg.Offset(), pg.PageSize)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[*CardBatch]{Items: items, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

type issueCardsBody struct {
	Kind      string          `json:"kind"`
	Quantity  int             `json:"quantity"`
	ExpiresAt *time.Time      `json:"expiresAt"`
	PlanIDs   []string        `json:"planIds"`
	Note      string          `json:"note"`
	Target    json.RawMessage `json:"target"`
}

type cardTargetBody struct {
	Type    string   `json:"type"`
	UserIDs []string `json:"userIds"`
	GroupID *string  `json:"groupId"`
	PlanID  *string  `json:"planId"`
}

// parseCardTarget decodes {type: users, userIds} | {type: group, groupId} |
// {type: plan, planId} | {type: all}.
func parseCardTarget(raw json.RawMessage, details map[string]any) CardTarget {
	var b cardTargetBody
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if isNullJSON(raw) || dec.Decode(&b) != nil {
		details["target"] = "必填：{type: users, userIds} | {type: group, groupId} | {type: plan, planId} | {type: all}"
		return CardTarget{}
	}
	t := CardTarget{Type: b.Type}
	parse := func(v, what string) *uuid.UUID {
		id, err := uuid.Parse(v)
		if err != nil {
			details["target"] = what + " 不是合法的 ID：" + v
			return nil
		}
		return &id
	}
	switch b.Type {
	case CardTargetUsers:
		for _, v := range b.UserIDs {
			id := parse(v, "userIds")
			if id == nil {
				return CardTarget{}
			}
			t.UserIDs = append(t.UserIDs, *id)
		}
	case CardTargetGroup:
		if b.GroupID != nil {
			t.GroupID = parse(*b.GroupID, "groupId")
		}
	case CardTargetPlan:
		if b.PlanID != nil {
			t.PlanID = parse(*b.PlanID, "planId")
		}
	}
	return t
}

func (h *Handler) issueCards(w http.ResponseWriter, r *http.Request) {
	var body issueCardsBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	in := IssueCardsInput{Kind: body.Kind, Quantity: body.Quantity, ExpiresAt: body.ExpiresAt, Note: body.Note,
		Target: parseCardTarget(body.Target, details)}
	for _, v := range body.PlanIDs {
		id, err := uuid.Parse(v)
		if err != nil {
			details["planIds"] = "不是合法的套餐 ID：" + v
			break
		}
		in.PlanIDs = append(in.PlanIDs, id)
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, invalid(details))
		return
	}
	dry := dryRun(r)
	res, err := h.svc.IssueCards(r.Context(), actor(r), in, dry, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusCreated
	if dry {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, res)
}

func (h *Handler) revokeCardBatch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("重置卡批次"))
		return
	}
	b, err := h.svc.RevokeCardBatch(r.Context(), actor(r), id, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, b)
}
