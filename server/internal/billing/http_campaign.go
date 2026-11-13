package billing

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/subscription"
)

func (h *Handler) PublicCampaignRoutes(r chi.Router) {
	r.Get("/public/benefits", h.publicCampaigns)
	r.Get("/public/benefits/{id}", h.publicCampaign)
}

func (h *Handler) campaignPublicView(r *http.Request, c *Campaign) (map[string]any, error) {
	requirements := []string{}
	if len(c.Conditions.GroupIDs) > 0 {
		requirements = append(requirements, "指定用户组")
	}
	if len(c.Conditions.UserIDs) > 0 {
		requirements = append(requirements, "指定用户")
	}
	if c.Conditions.RequirePurchase {
		requirements = append(requirements, "有付费套餐购买记录")
	}
	if c.Conditions.MinSpend != "" && c.Conditions.MinSpend != "0" {
		requirements = append(requirements, "累计模型消费达到 $"+c.Conditions.MinSpend+"（含套餐用量）")
	}
	if c.Conditions.RegisteredAfter != nil {
		requirements = append(requirements, "注册时间不早于 "+c.Conditions.RegisteredAfter.Format("2006-01-02 15:04 MST"))
	}
	if c.Conditions.RegisteredBefore != nil {
		requirements = append(requirements, "注册时间早于 "+c.Conditions.RegisteredBefore.Format("2006-01-02 15:04 MST"))
	}
	if c.Conditions.NeedsSubscription() {
		requirements = append(requirements, "持有符合条件的有效订阅")
	}
	if c.Conditions.QuotaKind != "" {
		label := map[string]string{"weekly": "周", "5h": "5 小时", "both": "5 小时或周"}[c.Conditions.QuotaKind]
		requirements = append(requirements, "订阅含"+label+"额度")
	}
	if c.Conditions.ExcludeHiddenPlans {
		requirements = append(requirements, "不含隐藏活动套餐")
	}
	// Only public plan names are disclosed; private user and group IDs remain hidden.
	for _, item := range []struct {
		ids   []uuid.UUID
		label string
	}{{c.Conditions.PlanIDs, "持有套餐："}, {c.Conditions.ExcludePlanIDs, "符合条件的订阅不含："}} {
		names := []string{}
		for _, pid := range item.ids {
			p, err := subscription.PlanTx(r.Context(), h.svc.pool, pid, h.svc.now(), false)
			if err != nil {
				return nil, err
			}
			names = append(names, p.Name)
		}
		if len(names) > 0 {
			requirements = append(requirements, item.label+strings.Join(names, "、"))
		}
	}
	if len(c.Conditions.Sources) > 0 {
		requirements = append(requirements, "订阅来源符合活动要求")
	}
	out := map[string]any{"id": c.ID, "title": c.Title, "description": c.Description, "rewardType": c.RewardType, "reward": c.Reward, "rewards": c.rewardItems(), "amount": c.Amount, "price": c.Price, "stock": c.Stock, "perUserLimit": c.PerUserLimit, "startsAt": c.StartsAt, "endsAt": c.EndsAt, "requirements": requirements}
	if c.PlanID != nil {
		p, err := subscription.PlanTx(r.Context(), h.svc.pool, *c.PlanID, h.svc.now(), false)
		if err != nil {
			return nil, err
		}
		out["plan"] = subscription.CatalogView(p)
		if p.Stock >= 0 && (c.Stock < 0 || p.Stock < c.Stock) {
			out["stock"] = p.Stock
		}
	}
	rewardViews := []map[string]any{}
	for _, reward := range c.rewardItems() {
		raw, _ := json.Marshal(reward)
		v := map[string]any{}
		_ = json.Unmarshal(raw, &v)
		if reward.PlanID != nil {
			p, err := subscription.PlanTx(r.Context(), h.svc.pool, *reward.PlanID, h.svc.now(), false)
			if err != nil {
				return nil, err
			}
			v["plan"] = subscription.CatalogView(p)
			if p.Stock >= 0 && (out["stock"].(int64) < 0 || p.Stock < out["stock"].(int64)) {
				out["stock"] = p.Stock
			}
		}
		rewardViews = append(rewardViews, v)
	}
	out["rewards"] = rewardViews
	return out, nil
}

func (h *Handler) publicCampaigns(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.Campaigns(r.Context(), false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := []map[string]any{}
	for _, c := range items {
		v, err := h.campaignPublicView(r, c)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, v)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) publicCampaign(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("活动"))
		return
	}
	c, err := scanCampaign(h.svc.pool.QueryRow(r.Context(), `SELECT `+campaignCols+` FROM benefit_campaigns WHERE id=$1 AND status='active'`, id))
	if db.IsNoRows(err) {
		err = apperr.NotFound("活动")
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.campaignPublicView(r, c)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) adminCampaigns(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.Campaigns(r.Context(), true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) saveCampaign(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Campaign
		// Identity comes from the URL (or is generated for POST). Older
		// clients submit id:"" for a new activity; do not decode it as UUID.
		ID           *string `json:"id"`
		Stock        *int64  `json:"stock"`
		PerUserLimit *int    `json:"perUserLimit"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body.Campaign.Stock = -1
	if body.Stock != nil {
		body.Campaign.Stock = *body.Stock
	}
	body.Campaign.PerUserLimit = 1
	if body.PerUserLimit != nil {
		body.Campaign.PerUserLimit = *body.PerUserLimit
	}
	if body.Status == "" {
		body.Status = "draft"
	}
	id := uuid.Nil
	if raw := chi.URLParam(r, "id"); raw != "" {
		var err error
		id, err = uuid.Parse(raw)
		if err != nil {
			httpx.WriteError(w, r, apperr.NotFound("活动"))
			return
		}
	}
	out, err := h.svc.SaveCampaign(r.Context(), actor(r), id, body.Campaign, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) claimCampaign(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestID     uuid.UUID `json:"requestId"`
		ExpectedPrice string    `json:"expectedPrice"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.ExpectedPrice == "" {
		body.ExpectedPrice = "0"
	}
	price, err := money.Parse(body.ExpectedPrice)
	if err != nil || price < 0 {
		httpx.WriteError(w, r, apperr.Validation("价格无效", nil))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("活动"))
		return
	}
	out, err := h.svc.ClaimCampaign(r.Context(), actor(r), id, body.RequestID, price, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) myCampaignClaims(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.pool.Query(r.Context(), `SELECT campaign_id,count(*) FROM benefit_claims WHERE user_id=$1 GROUP BY campaign_id`, actor(r).ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err = rows.Scan(&id, &n); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		counts[id] = n
	}
	if err = rows.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"counts": counts})
}
