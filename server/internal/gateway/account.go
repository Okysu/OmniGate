package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/keys"
	"omnigate/internal/limits"
	"omnigate/internal/money"
	"omnigate/internal/protocol"
	"omnigate/internal/subscription"
)

// Account endpoints: a client holding only an API key reads its account's
// balance and plan quotas (GET /v1/account), and OpenAI-compatible clients
// that show a balance (Cherry Studio, NextChat, …) read the legacy
// /dashboard/billing endpoints.

// AccountSource supplies the account data (implemented in internal/app).
type AccountSource interface {
	// Wallet returns the user's balance, the amount held by requests in
	// flight, and the currency.
	Wallet(ctx context.Context, userID uuid.UUID) (balance, reserved money.Amount, currency string, err error)
	// ActiveSubscriptions returns the valid subscriptions with current usage,
	// in the order requests use them.
	ActiveSubscriptions(ctx context.Context, userID uuid.UUID) ([]*subscription.SubscriptionView, error)
	// QuotaOverflow is the user's choice once plan quota is used up
	// (block | wallet).
	QuotaOverflow(ctx context.Context, userID uuid.UUID) (string, error)
}

type accountKeyJSON struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expiresAt"`
	// SpendLimit, Spent and ResetsAt are the key's own spend cap and its
	// charge in the current window (null without a limit).
	SpendLimit *keys.SpendLimit `json:"spendLimit"`
	Spent      *string          `json:"spent"`
	ResetsAt   *time.Time       `json:"resetsAt"`
}

type accountWalletJSON struct {
	Balance   string `json:"balance"`
	Reserved  string `json:"reserved"`
	Available string `json:"available"`
	Currency  string `json:"currency"`
}

// accountJSON is the GET /v1/account document.
type accountJSON struct {
	Object string `json:"object"` // "account"
	User   struct {
		ID          uuid.UUID `json:"id"`
		DisplayName string    `json:"displayName"`
	} `json:"user"`
	Key    accountKeyJSON    `json:"key"`
	Wallet accountWalletJSON `json:"wallet"`
	// QuotaOverflow is what this key does once plan quota is used up:
	// block, or pay from the wallet (the key's own setting, else the user's).
	QuotaOverflow string `json:"quotaOverflow"`
	// Subscriptions are the valid plans in the order requests use them, each
	// rule with its used / remaining amount and reset time.
	Subscriptions []*subscription.SubscriptionView `json:"subscriptions"`
	// Limits are the user group's limits and usage (null when not enforced).
	Limits *limits.Overview `json:"limits"`
}

// AccountRoutes registers the account endpoints on the /v1 router.
func (g *Gateway) AccountRoutes(r chi.Router) {
	r.Get("/account", g.account)
	g.DashboardRoutes(r)
}

// DashboardRoutes registers the OpenAI-compatible legacy billing endpoints
// (also mounted at the root: clients differ on the /v1 prefix).
func (g *Gateway) DashboardRoutes(r chi.Router) {
	r.Get("/dashboard/billing/subscription", g.dashboardSubscription)
	r.Get("/dashboard/billing/usage", g.dashboardUsage)
}

// accountAuth authenticates the key of an account request; nil after
// writing the error.
func (g *Gateway) accountAuth(w http.ResponseWriter, r *http.Request) *keys.Auth {
	if g.opts.Account == nil {
		writeError(w, r, protocol.OpenAIChat, protocol.NewError(protocol.ErrInvalidRequest, "unknown endpoint "+r.URL.Path))
		return nil
	}
	a, gerr := g.authenticate(r)
	if gerr != nil {
		writeError(w, r, protocol.OpenAIChat, gerr)
		return nil
	}
	return a
}

func (g *Gateway) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	g.log.ErrorContext(r.Context(), what, "err", err)
	writeError(w, r, protocol.OpenAIChat, protocol.NewError(protocol.ErrInternal, "internal error"))
}

func (g *Gateway) account(w http.ResponseWriter, r *http.Request) {
	a := g.accountAuth(w, r)
	if a == nil {
		return
	}
	ctx := r.Context()
	out := accountJSON{Object: "account", Key: accountKeyJSON{ID: a.Key.ID, Name: a.Key.Name, ExpiresAt: a.Key.ExpiresAt, SpendLimit: a.Key.Policy.SpendLimit}}
	out.User.ID, out.User.DisplayName = a.UserID, a.UserName
	bal, res, cur, err := g.opts.Account.Wallet(ctx, a.UserID)
	if err != nil {
		g.internalError(w, r, "account wallet failed", err)
		return
	}
	out.Wallet = accountWalletJSON{Balance: bal.String(), Reserved: res.String(), Available: (bal - res).String(), Currency: cur}
	if out.Subscriptions, err = g.opts.Account.ActiveSubscriptions(ctx, a.UserID); err != nil {
		g.internalError(w, r, "account subscriptions failed", err)
		return
	}
	out.QuotaOverflow = a.Key.Policy.QuotaOverflow
	if out.QuotaOverflow == "" {
		if out.QuotaOverflow, err = g.opts.Account.QuotaOverflow(ctx, a.UserID); err != nil {
			g.internalError(w, r, "account preferences failed", err)
			return
		}
	}
	if g.opts.Usage != nil {
		ov, err := g.opts.Usage.Overview(ctx, a.UserID, []*keys.Key{a.Key})
		if err != nil {
			g.internalError(w, r, "account limits failed", err)
			return
		}
		if len(ov.Keys) == 1 {
			out.Key.Spent, out.Key.ResetsAt = ov.Keys[0].Spent, ov.Keys[0].ResetsAt
		}
		out.Limits = ov
	}
	writeJSON(w, out)
}

// dashboardSubscription answers OpenAI's legacy billing endpoint with the
// available wallet balance as the hard limit (plan quotas have no place in
// this format; GET /v1/account has them).
func (g *Gateway) dashboardSubscription(w http.ResponseWriter, r *http.Request) {
	a := g.accountAuth(w, r)
	if a == nil {
		return
	}
	bal, res, _, err := g.opts.Account.Wallet(r.Context(), a.UserID)
	if err != nil {
		g.internalError(w, r, "dashboard wallet failed", err)
		return
	}
	avail := json.Number((bal - res).String())
	expires := int64(0)
	if a.Key.ExpiresAt != nil {
		expires = a.Key.ExpiresAt.Unix()
	}
	writeJSON(w, map[string]any{
		"object": "billing_subscription", "has_payment_method": true,
		"soft_limit_usd": avail, "hard_limit_usd": avail, "system_hard_limit_usd": avail,
		"access_until": expires,
	})
}

// dashboardUsage reports zero usage: clients show hard_limit_usd − usage as
// the remaining balance, which then is the available wallet balance.
func (g *Gateway) dashboardUsage(w http.ResponseWriter, r *http.Request) {
	if g.accountAuth(w, r) == nil {
		return
	}
	writeJSON(w, map[string]any{"object": "list", "total_usage": 0})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
