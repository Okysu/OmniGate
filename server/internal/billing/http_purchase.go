package billing

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/money"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/subscription"
)

// Wallet purchases and referral endpoints (docs/contracts/phase15-api.md §3, §4.4).
func (h *Handler) purchaseRoutes(r chi.Router) {
	r.Get("/billing/purchase/options", h.purchaseOptions)
	r.Post("/billing/purchase", h.purchase)
	r.Get("/billing/purchases", h.purchases)
	r.Get("/billing/referral", h.referral)
	r.Get("/billing/referral/rebates", h.referralRebates)
}

func amountPtr(a *money.Amount) *string {
	if a == nil {
		return nil
	}
	s := a.String()
	return &s
}

type upgradeJSON struct {
	FromSubscriptionID uuid.UUID `json:"fromSubscriptionId"`
	FromPlanID         uuid.UUID `json:"fromPlanId"`
	FromPlanName       string    `json:"fromPlanName"`
	FromPrice          *string   `json:"fromPrice"`
	Price              string    `json:"price"`
	Credit             string    `json:"credit"`
	RemainingSeconds   int64     `json:"remainingSeconds"`
	EndsAt             time.Time `json:"endsAt"`
}

type purchaseOptionJSON struct {
	Plan                subscription.CatalogPlanJSON `json:"plan"`
	Purchasable         bool                         `json:"purchasable"`
	Action              string                       `json:"action"`
	Price               *string                      `json:"price"`
	RenewSubscriptionID *uuid.UUID                   `json:"renewSubscriptionId"`
	CurrentEndsAt       *time.Time                   `json:"currentEndsAt"`
	NewEndsAt           *time.Time                   `json:"newEndsAt"`
	Upgrades            []upgradeJSON                `json:"upgrades"`
}

type activeSubJSON struct {
	ID       uuid.UUID `json:"id"`
	PlanID   uuid.UUID `json:"planId"`
	PlanName string    `json:"planName"`
	Models   []string  `json:"models"`
	EndsAt   time.Time `json:"endsAt"`
}

type purchaseOptionsJSON struct {
	Available     string               `json:"available"`
	Currency      string               `json:"currency"`
	Plans         []purchaseOptionJSON `json:"plans"`
	Subscriptions []activeSubJSON      `json:"subscriptions"`
}

func (h *Handler) purchaseOptions(w http.ResponseWriter, r *http.Request) {
	opts, err := h.svc.PurchaseOptions(r.Context(), auth.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := purchaseOptionsJSON{Available: opts.Available.String(), Currency: opts.Currency, Plans: []purchaseOptionJSON{}, Subscriptions: []activeSubJSON{}}
	for _, s := range opts.Active {
		out.Subscriptions = append(out.Subscriptions, activeSubJSON{ID: s.ID, PlanID: s.PlanID, PlanName: s.PlanName, Models: s.Models, EndsAt: s.EndsAt})
	}
	for _, o := range opts.Plans {
		v := purchaseOptionJSON{Plan: subscription.CatalogView(o.Plan), Purchasable: o.Purchasable, Action: o.Action,
			Price: amountPtr(o.Price), RenewSubscriptionID: o.RenewSubscriptionID, CurrentEndsAt: o.CurrentEndsAt,
			NewEndsAt: o.NewEndsAt, Upgrades: []upgradeJSON{}}
		for _, u := range o.Upgrades {
			v.Upgrades = append(v.Upgrades, upgradeJSON{FromSubscriptionID: u.FromSubscriptionID, FromPlanID: u.FromPlanID,
				FromPlanName: u.FromPlanName, FromPrice: amountPtr(u.FromPrice), Price: u.Price.String(), Credit: u.Credit.String(),
				RemainingSeconds: int64(u.Remaining / time.Second), EndsAt: u.EndsAt})
		}
		out.Plans = append(out.Plans, v)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type purchaseBody struct {
	PlanID             string  `json:"planId"`
	FromSubscriptionID *string `json:"fromSubscriptionId"`
	ExpectedPrice      string  `json:"expectedPrice"`
}

type purchaseResultJSON struct {
	ID           uuid.UUID                      `json:"id"`
	Action       string                         `json:"action"`
	Price        string                         `json:"price"`
	Wallet       walletJSON                     `json:"wallet"`
	Subscription *subscription.SubscriptionView `json:"subscription"`
}

func (h *Handler) purchase(w http.ResponseWriter, r *http.Request) {
	var body purchaseBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	in := PurchaseInput{}
	if id, err := uuid.Parse(body.PlanID); err != nil {
		details["planId"] = "必须是合法的 UUID"
	} else {
		in.PlanID = id
	}
	if body.FromSubscriptionID != nil && *body.FromSubscriptionID != "" {
		if id, err := uuid.Parse(*body.FromSubscriptionID); err != nil {
			details["fromSubscriptionId"] = "必须是合法的 UUID"
		} else {
			in.FromSubscriptionID = &id
		}
	}
	in.ExpectedPrice = parseAmount("expectedPrice", body.ExpectedPrice, details)
	if len(details) > 0 {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", details))
		return
	}
	res, err := h.svc.Purchase(r.Context(), actor(r), in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, purchaseResultJSON{ID: res.ID, Action: res.Action, Price: res.Price.String(),
		Wallet: walletView(res.Wallet), Subscription: res.Subscription})
}

type purchaseRecordJSON struct {
	ID             uuid.UUID  `json:"id"`
	Action         string     `json:"action"`
	PlanID         uuid.UUID  `json:"planId"`
	PlanName       string     `json:"planName"`
	FromPlanName   *string    `json:"fromPlanName"`
	Price          string     `json:"price"`
	SubscriptionID *uuid.UUID `json:"subscriptionId"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func (h *Handler) purchases(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	items, total, err := h.svc.ListPurchases(r.Context(), auth.PrincipalFrom(r.Context()).UserID, pg.Offset(), pg.PageSize)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]purchaseRecordJSON, len(items))
	for i, p := range items {
		out[i] = purchaseRecordJSON{ID: p.ID, Action: p.Action, PlanID: p.PlanID, PlanName: p.PlanName,
			FromPlanName: p.FromPlanName, Price: p.Price.String(), SubscriptionID: p.SubscriptionID, CreatedAt: p.CreatedAt}
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[purchaseRecordJSON]{Items: out, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

type inviteeJSON struct {
	DisplayName string    `json:"displayName"`
	JoinedAt    time.Time `json:"joinedAt"`
	RebateTotal string    `json:"rebateTotal"`
}

type referralJSON struct {
	Enabled      bool          `json:"enabled"`
	Rate         string        `json:"rate"`
	MinRecharge  string        `json:"minRecharge"`
	Code         string        `json:"code"`
	Link         string        `json:"link"`
	InvitedCount int           `json:"invitedCount"`
	RebateTotal  string        `json:"rebateTotal"`
	Invitees     []inviteeJSON `json:"invitees"`
}

// InviteLink is {publicURL}/login?invite=code.
func InviteLink(publicURL, code string) string {
	return strings.TrimRight(publicURL, "/") + "/login?invite=" + url.QueryEscape(code)
}

func (h *Handler) referral(w http.ResponseWriter, r *http.Request) {
	info, err := h.svc.ReferralInfo(r.Context(), auth.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := referralJSON{Enabled: info.Config.Enabled, Rate: info.Config.Rate, MinRecharge: info.Config.MinRecharge.String(),
		Code: info.Code, Link: InviteLink(h.PublicURL, info.Code), InvitedCount: info.InvitedCount,
		RebateTotal: info.RebateTotal.String(), Invitees: []inviteeJSON{}}
	for _, v := range info.Invitees {
		out.Invitees = append(out.Invitees, inviteeJSON{DisplayName: v.DisplayName, JoinedAt: v.JoinedAt, RebateTotal: v.RebateTotal.String()})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type rebateJSON struct {
	ID          uuid.UUID `json:"id"`
	InviteeName string    `json:"inviteeName"`
	Recharge    string    `json:"recharge"`
	Rate        string    `json:"rate"`
	Rebate      string    `json:"rebate"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (h *Handler) referralRebates(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	items, total, err := h.svc.ListReferralRebates(r.Context(), auth.PrincipalFrom(r.Context()).UserID, pg.Offset(), pg.PageSize)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]rebateJSON, len(items))
	for i, b := range items {
		out[i] = rebateJSON{ID: b.ID, InviteeName: b.InviteeName, Recharge: b.Recharge.String(), Rate: b.Rate,
			Rebate: b.Rebate.String(), CreatedAt: b.CreatedAt}
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[rebateJSON]{Items: out, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}
