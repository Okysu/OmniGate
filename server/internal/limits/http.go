package limits

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/auth"
	"omnigate/internal/keys"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/usergroup"
)

// Handler serves GET /api/billing/limits (§2.3).
type Handler struct {
	svc  *Service
	keys *keys.Service
}

func NewHandler(svc *Service, k *keys.Service) *Handler { return &Handler{svc: svc, keys: k} }

// Routes mounts the endpoint on the /api router (any signed-in user).
func (h *Handler) Routes(r chi.Router) { r.Get("/billing/limits", h.get) }

type groupLimitsJSON struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	PriceMultiplier string    `json:"priceMultiplier"`
	Timezone        string    `json:"timezone"`
	usergroup.LimitsJSON
}

type resetsJSON struct {
	Day   time.Time `json:"day"`
	Month time.Time `json:"month"`
}

type usageJSON struct {
	RPDUsed      int64      `json:"rpdUsed"`
	DailySpent   string     `json:"dailySpent"`
	MonthlySpent string     `json:"monthlySpent"`
	ResetsAt     resetsJSON `json:"resetsAt"`
}

type keyLimitJSON struct {
	ID         uuid.UUID        `json:"id"`
	Name       string           `json:"name"`
	SpendLimit *keys.SpendLimit `json:"spendLimit"`
	// Spent is the key's charge in the current window of its limit (null
	// without a limit); ResetsAt is null for 'total' and without a limit.
	Spent    *string    `json:"spent"`
	ResetsAt *time.Time `json:"resetsAt"`
}

// Overview is the GET /api/billing/limits document.
type Overview struct {
	Group groupLimitsJSON `json:"group"`
	Usage usageJSON       `json:"usage"`
	Keys  []keyLimitJSON  `json:"keys"`
}

// Overview returns userID's limits and usage at now.
func (s *Service) Overview(ctx context.Context, userID uuid.UUID, userKeys []*keys.Key) (*Overview, error) {
	g, err := s.groups.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	now, loc := s.now(), g.Location()
	out := &Overview{Group: groupLimitsJSON{ID: g.ID, Name: g.Name, PriceMultiplier: g.Multiplier.String(), Timezone: g.Timezone,
		LimitsJSON: g.Limits.JSON()}, Keys: []keyLimitJSON{}}
	reqs, daily, err := read(ctx, s.pool, ScopeUser, userID, Day, Start(Day, now, loc))
	if err != nil {
		return nil, err
	}
	_, monthly, err := read(ctx, s.pool, ScopeUser, userID, Month, Start(Month, now, loc))
	if err != nil {
		return nil, err
	}
	out.Usage = usageJSON{RPDUsed: reqs, DailySpent: daily.String(), MonthlySpent: monthly.String(),
		ResetsAt: resetsJSON{Day: *End(Day, now, loc), Month: *End(Month, now, loc)}}
	for _, k := range userKeys {
		kl := keyLimitJSON{ID: k.ID, Name: k.Name, SpendLimit: k.Policy.SpendLimit}
		if l := k.Policy.SpendLimit; l != nil && Window(l.Window).Valid() {
			w := Window(l.Window)
			_, spent, err := read(ctx, s.pool, ScopeKey, k.ID, w, Start(w, now, loc))
			if err != nil {
				return nil, err
			}
			v := spent.String()
			kl.Spent, kl.ResetsAt = &v, End(w, now, loc)
		}
		out.Keys = append(out.Keys, kl)
	}
	return out, nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	userKeys, err := h.keys.List(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.svc.Overview(r.Context(), p.UserID, userKeys)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
