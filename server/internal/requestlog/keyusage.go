package requestlog

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
)

// keyUsageRanges are the windows of GET /api/stats/keys/{id}/usage; "all"
// covers every retained log.
var keyUsageRanges = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "all": 0}

type keyTokens struct {
	Requests         int64  `json:"requests"`
	InputTokens      int64  `json:"inputTokens"`
	CacheReadTokens  int64  `json:"cacheReadTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	ReasoningTokens  int64  `json:"reasoningTokens"`
	TotalTokens      int64  `json:"totalTokens"`
	Charge           string `json:"charge"`
}

type keyModelTokens struct {
	Model string `json:"model"`
	keyTokens
}

// keyUsage is the token usage of one API key over a window, in total and per
// model. Only model requests count; inputTokens excludes cache tokens, and
// totalTokens = input + cache read + cache write + output.
func (h *Handler) keyUsage(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("API Key"))
		return
	}
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "24h"
	}
	window, ok := keyUsageRanges[rng]
	if !ok {
		httpx.WriteError(w, r, apperr.Validation("range 只能是 24h、7d、30d、all", nil))
		return
	}
	ctx := r.Context()
	var owner uuid.UUID
	if err := h.pool.QueryRow(ctx, `SELECT user_id FROM gateway_keys WHERE id = $1`, id).Scan(&owner); err != nil {
		if db.IsNoRows(err) {
			err = apperr.NotFound("API Key")
		}
		httpx.WriteError(w, r, err)
		return
	}
	if owner != p.UserID && !p.Can(authz.StatsAll) {
		httpx.WriteError(w, r, apperr.NotFound("API Key"))
		return
	}
	to := time.Now().UTC()
	var from *time.Time
	where, args := ` WHERE l.user_id = $1 AND l.key_id = $2 AND `+modelRequest, []any{owner, id}
	if window > 0 {
		t := to.Add(-window)
		from = &t
		where += ` AND l.started_at >= $3`
		args = append(args, t)
	}
	items, err := h.keyModelTokens(ctx, where, args)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	totals := keyTokens{Charge: "0"}
	for _, m := range items {
		totals.Requests += m.Requests
		totals.InputTokens += m.InputTokens
		totals.CacheReadTokens += m.CacheReadTokens
		totals.CacheWriteTokens += m.CacheWriteTokens
		totals.OutputTokens += m.OutputTokens
		totals.ReasoningTokens += m.ReasoningTokens
		totals.TotalTokens += m.TotalTokens
	}
	if len(items) > 0 {
		var sum *string
		if err := h.pool.QueryRow(ctx, `SELECT `+h.pool.Dialect().SumText("charge_nano")+` FROM request_logs l`+where, args...).Scan(&sum); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		totals.Charge = numericString(sum)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"keyId": id, "range": rng, "from": from, "to": to, "totals": totals, "byModel": items})
}

func (h *Handler) keyModelTokens(ctx context.Context, where string, args []any) ([]keyModelTokens, error) {
	rows, err := h.pool.Query(ctx, `SELECT model, count(*), coalesce(sum(input_tokens), 0), coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_write_tokens), 0), coalesce(sum(output_tokens), 0), coalesce(sum(reasoning_tokens), 0),
		`+h.pool.Dialect().SumText("charge_nano")+`
		FROM request_logs l`+where+` GROUP BY model
		ORDER BY coalesce(sum(input_tokens + cache_read_tokens + cache_write_tokens + output_tokens), 0) DESC, count(*) DESC, model`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []keyModelTokens{}
	for rows.Next() {
		var m keyModelTokens
		var c *string
		if err := rows.Scan(&m.Model, &m.Requests, &m.InputTokens, &m.CacheReadTokens, &m.CacheWriteTokens, &m.OutputTokens,
			&m.ReasoningTokens, &c); err != nil {
			return nil, err
		}
		m.TotalTokens = m.InputTokens + m.CacheReadTokens + m.CacheWriteTokens + m.OutputTokens
		m.Charge = numericString(c)
		out = append(out, m)
	}
	return out, rows.Err()
}
