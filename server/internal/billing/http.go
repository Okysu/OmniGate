package billing

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/money"
	"omnigate/internal/platform/httpx"
)

// Handler serves /api/billing/* (Routes) and /api/admin/billing/* (AdminRoutes).
type Handler struct {
	svc *Service
	// PublicURL is the external base URL (invite links); set before use.
	PublicURL string
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers the user endpoints; mount on the /api router.
func (h *Handler) Routes(r chi.Router) {
	own := r.With(auth.Require(authz.BillingOwn))
	own.Get("/billing/wallet", h.wallet)
	own.Get("/billing/ledger", h.ledger)
	own.Post("/billing/redeem", h.redeem)
	h.purchaseRoutes(own)
}

// AdminRoutes registers the management endpoints; mount on the /api/admin router.
func (h *Handler) AdminRoutes(r chi.Router) {
	m := r.With(auth.Require(authz.BillingManage))
	m.Get("/billing/settings", h.getSettings)
	m.Put("/billing/settings", h.putSettings)
	m.Get("/billing/redeem-batches", h.listBatches)
	m.Post("/billing/redeem-batches", h.createBatch)
	m.Patch("/billing/redeem-batches/{id}", h.patchBatch)
	m.Get("/billing/wallets/{userId}", h.adminWallet)
	m.Post("/billing/wallets/{userId}/adjust", h.adjust)
}

// ---- JSON views (money is always a decimal string) ----

type walletJSON struct {
	UserID    uuid.UUID `json:"userId"`
	Balance   string    `json:"balance"`
	Reserved  string    `json:"reserved"`
	Available string    `json:"available"`
	Currency  string    `json:"currency"`
	Version   int       `json:"version"`
}

func walletView(w Wallet) walletJSON {
	return walletJSON{UserID: w.UserID, Balance: w.Balance.String(), Reserved: w.Reserved.String(),
		Available: w.Available().String(), Currency: w.Currency, Version: w.Version}
}

type ledgerJSON struct {
	ID           uuid.UUID `json:"id"`
	Kind         string    `json:"kind"`
	Amount       string    `json:"amount"`
	BalanceAfter string    `json:"balanceAfter"`
	RefType      string    `json:"refType"`
	RefID        string    `json:"refId"`
	Note         *string   `json:"note"`
	CreatedAt    time.Time `json:"createdAt"`
}

func ledgerView(e *LedgerEntry) ledgerJSON {
	return ledgerJSON{ID: e.ID, Kind: e.Kind, Amount: e.Amount.String(), BalanceAfter: e.BalanceAfter.String(),
		RefType: e.RefType, RefID: e.RefID, Note: e.Note, CreatedAt: e.CreatedAt}
}

type batchJSON struct {
	ID                    uuid.UUID  `json:"id"`
	Kind                  string     `json:"kind"`
	Amount                *string    `json:"amount"`   // wallet_credit
	PlanID                *uuid.UUID `json:"planId"`   // plan
	PlanName              *string    `json:"planName"` // plan
	Periods               *int       `json:"periods"`  // plan
	Count                 int        `json:"count"`
	Redeemed              int        `json:"redeemed"`
	MaxRedemptionsPerCode int        `json:"maxRedemptionsPerCode"`
	PerUserLimit          int        `json:"perUserLimit"`
	ValidFrom             *time.Time `json:"validFrom"`
	ExpiresAt             *time.Time `json:"expiresAt"`
	Note                  *string    `json:"note"`
	Status                string     `json:"status"`
	CreatedAt             time.Time  `json:"createdAt"`
	CreatedBy             uuid.UUID  `json:"createdBy"`
	Version               int        `json:"version"`
}

func batchView(b *Batch) batchJSON {
	var amount *string
	if b.Kind == BatchKindWalletCredit {
		amount = ptr(b.Amount.String())
	}
	return batchJSON{ID: b.ID, Kind: b.Kind, Amount: amount, PlanID: b.PlanID, PlanName: b.PlanName, Periods: b.Periods,
		Count: b.Count, Redeemed: b.Redeemed,
		MaxRedemptionsPerCode: b.MaxRedemptionsPerCode, PerUserLimit: b.PerUserLimit, ValidFrom: b.ValidFrom,
		ExpiresAt: b.ExpiresAt, Note: b.Note, Status: b.Status, CreatedAt: b.CreatedAt, CreatedBy: b.CreatedBy,
		Version: b.Version}
}

// ---- helpers ----

func meta(r *http.Request) RequestMeta {
	return RequestMeta{IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), RequestID: httpx.RequestID(r.Context())}
}

func actor(r *http.Request) Actor {
	p := auth.PrincipalFrom(r.Context())
	return Actor{ID: p.UserID, Name: p.Name}
}

func parseAmount(field, s string, details map[string]any) money.Amount {
	if s == "" {
		details[field] = "必填"
		return 0
	}
	a, err := money.Parse(s)
	if err != nil {
		details[field] = "不是合法的金额（十进制字符串，最多 9 位小数）"
	}
	return a
}

// ---- user endpoints ----

func (h *Handler) wallet(w http.ResponseWriter, r *http.Request) {
	wl, err := h.svc.GetWallet(r.Context(), auth.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, walletView(wl))
}

func (h *Handler) ledger(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	items, total, err := h.svc.ListLedger(r.Context(), auth.PrincipalFrom(r.Context()).UserID, pg.Offset(), pg.PageSize)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]ledgerJSON, len(items))
	for i := range items {
		out[i] = ledgerView(&items[i])
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[ledgerJSON]{Items: out, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

type redeemBody struct {
	Code string `json:"code"`
}

func (h *Handler) redeem(w http.ResponseWriter, r *http.Request) {
	var body redeemBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Redeem(r.Context(), actor(r), body.Code, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if res.Kind == BatchKindPlan {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"kind": res.Kind, "subscription": res.Subscription})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"kind": res.Kind, "amount": res.Amount.String(), "wallet": walletView(res.Wallet)})
}

// ---- admin endpoints ----

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Settings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

type settingsBody struct {
	Enforce *bool `json:"enforce"`
	Version *int  `json:"version"`
}

func (h *Handler) putSettings(w http.ResponseWriter, r *http.Request) {
	var body settingsBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	if body.Enforce == nil {
		details["enforce"] = "必填"
	}
	if body.Version == nil {
		details["version"] = "必填"
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", details))
		return
	}
	st, err := h.svc.UpdateSettings(r.Context(), actor(r), *body.Enforce, *body.Version, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (h *Handler) listBatches(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	items, total, err := h.svc.ListBatches(r.Context(), pg.Offset(), pg.PageSize)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]batchJSON, len(items))
	for i, b := range items {
		out[i] = batchView(b)
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[batchJSON]{Items: out, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

type createBatchBody struct {
	Kind                  string     `json:"kind"` // "" → wallet_credit
	Amount                string     `json:"amount"`
	PlanID                *uuid.UUID `json:"planId"`
	Periods               *int       `json:"periods"`
	Count                 int        `json:"count"`
	MaxRedemptionsPerCode *int       `json:"maxRedemptionsPerCode"`
	PerUserLimit          *int       `json:"perUserLimit"`
	ValidFrom             *time.Time `json:"validFrom"`
	ExpiresAt             *time.Time `json:"expiresAt"`
	Note                  *string    `json:"note"`
}

func (h *Handler) createBatch(w http.ResponseWriter, r *http.Request) {
	var body createBatchBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	in := BatchInput{Kind: body.Kind, PlanID: body.PlanID, Count: body.Count, ValidFrom: body.ValidFrom, ExpiresAt: body.ExpiresAt}
	switch body.Kind {
	case BatchKindPlan:
		if body.Amount != "" {
			details["amount"] = "plan 类型不能设置金额"
		}
		if body.Periods == nil {
			details["periods"] = "必填"
		} else {
			in.Periods = *body.Periods
		}
	default:
		in.Amount = parseAmount("amount", body.Amount, details)
		if body.Periods != nil {
			details["periods"] = "仅 plan 类型可用"
		}
	}
	if body.MaxRedemptionsPerCode != nil {
		if in.MaxRedemptionsPerCode = *body.MaxRedemptionsPerCode; in.MaxRedemptionsPerCode < 1 {
			details["maxRedemptionsPerCode"] = "必须大于 0"
		}
	}
	if body.PerUserLimit != nil {
		if in.PerUserLimit = *body.PerUserLimit; in.PerUserLimit < 1 {
			details["perUserLimit"] = "必须大于 0"
		}
	}
	if body.Note != nil {
		n := strings.TrimSpace(*body.Note)
		in.Note = &n
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", details))
		return
	}
	b, codes, err := h.svc.CreateBatch(r.Context(), actor(r), in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"batch": batchView(b), "codes": codes})
}

type patchBatchBody struct {
	Status  *string `json:"status"`
	Version *int    `json:"version"`
}

func (h *Handler) patchBatch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("兑换码批次"))
		return
	}
	var body patchBatchBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Status == nil {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", map[string]any{"status": "必填"}))
		return
	}
	b, err := h.svc.UpdateBatchStatus(r.Context(), actor(r), id, *body.Status, body.Version, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, batchView(b))
}

func (h *Handler) adminWallet(w http.ResponseWriter, r *http.Request) {
	uid, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("用户"))
		return
	}
	wl, err := h.svc.AdminWallet(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, walletView(wl))
}

type adjustBody struct {
	Amount  string `json:"amount"`
	Note    string `json:"note"`
	Version *int   `json:"version"`
}

func (h *Handler) adjust(w http.ResponseWriter, r *http.Request) {
	uid, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("用户"))
		return
	}
	var body adjustBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	amount := parseAmount("amount", body.Amount, details)
	if body.Version == nil {
		details["version"] = "必填"
	}
	if strings.TrimSpace(body.Note) == "" {
		details["note"] = "必填"
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", details))
		return
	}
	wl, entry, err := h.svc.AdjustWallet(r.Context(), actor(r), uid,
		AdjustInput{Amount: amount, Note: strings.TrimSpace(body.Note), Version: *body.Version}, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"wallet": walletView(wl), "entry": ledgerView(entry)})
}
