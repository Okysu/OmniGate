package requestlog

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
)

type Handler struct{ pool *db.DB }

func NewHandler(pool *db.DB) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	r.With(auth.Require(authz.StatsOwn)).Get("/logs", h.list)
	r.With(auth.Require(authz.StatsOwn)).Get("/stats/summary", h.summary)
}

type filter struct {
	from, to time.Time
	where    []string
	args     []any
	seeAll   bool
}

func (f *filter) add(cond string, v any) {
	f.args = append(f.args, v)
	f.where = append(f.where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(f.args))))
}

func (f *filter) sql() string { return " WHERE " + strings.Join(f.where, " AND ") }

// parseFilter applies time range and visibility. Users without stats.all only
// see their own requests; userId is honoured only with stats.all.
func parseFilter(r *http.Request, maxDays int) (*filter, error) {
	p := auth.PrincipalFrom(r.Context())
	q := r.URL.Query()
	f := &filter{to: time.Now().UTC(), seeAll: p.Can(authz.StatsAll)}
	f.from = f.to.AddDate(0, 0, -7)
	for name, dst := range map[string]*time.Time{"from": &f.from, "to": &f.to} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, apperr.Validation(name+" 必须是 RFC 3339 时间", nil)
			}
			*dst = t.UTC()
		}
	}
	if !f.from.Before(f.to) || f.to.Sub(f.from) > time.Duration(maxDays)*24*time.Hour {
		return nil, apperr.Validation("时间范围无效（最长 "+strconv.Itoa(maxDays)+" 天）", nil)
	}
	f.add("l.started_at >= ?", f.from)
	f.add("l.started_at < ?", f.to)
	if f.seeAll {
		if v := q.Get("userId"); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				return nil, apperr.Validation("userId 不是合法的 UUID", nil)
			}
			f.add("l.user_id = ?", id)
		}
	} else {
		f.add("l.user_id = ?", p.UserID)
	}
	for name, col := range map[string]string{"channelId": "l.channel_id", "keyId": "l.key_id"} {
		if v := q.Get(name); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				return nil, apperr.Validation(name+" 不是合法的 UUID", nil)
			}
			f.add(col+" = ?", id)
		}
	}
	if v := q.Get("model"); v != "" {
		f.add("l.model = ?", v)
	}
	switch q.Get("status") {
	case "success":
		f.where = append(f.where, "l.status_code < 400")
	case "error":
		f.where = append(f.where, "l.status_code >= 400")
	}
	return f, nil
}

type logUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Reasoning  int64 `json:"reasoning"`
	Estimated  bool  `json:"estimated"`
	// ImageInputTokens is the image part of Input (image endpoints; phase7-api.md §1.1).
	ImageInputTokens int64 `json:"imageInputTokens"`
	// AudioInputTokens / AudioOutputTokens are the audio parts of Input /
	// Output; InputCharacters is the billed input size of a speech request
	// (audio endpoints; phase9-api.md §1.1).
	AudioInputTokens  int64 `json:"audioInputTokens"`
	AudioOutputTokens int64 `json:"audioOutputTokens"`
	InputCharacters   int64 `json:"inputCharacters"`
}

type userRef struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"displayName"`
}

type logView struct {
	ID             uuid.UUID       `json:"id"`
	RequestID      string          `json:"requestId"`
	StartedAt      time.Time       `json:"startedAt"`
	User           *userRef        `json:"user"`
	KeyID          *uuid.UUID      `json:"keyId"`
	KeyName        *string         `json:"keyName"`
	Inbound        string          `json:"inbound"`
	Model          string          `json:"model"`
	ServedModel    string          `json:"servedModel"`
	ChannelID      *uuid.UUID      `json:"channelId"`
	ChannelName    *string         `json:"channelName"`
	UpstreamModel  *string         `json:"upstreamModel"`
	Stream         bool            `json:"stream"`
	StatusCode     int             `json:"statusCode"`
	ErrorClass     *string         `json:"errorClass"`
	ErrorMessage   *string         `json:"errorMessage"`
	Attempts       int             `json:"attempts"`
	FallbackPath   json.RawMessage `json:"fallbackPath"`
	TTFTMs         *int64          `json:"ttftMs"`
	DurationMs     int64           `json:"durationMs"`
	Usage          logUsage        `json:"usage"`
	Cost           *string         `json:"cost"`
	Charge         string          `json:"charge"`
	SubscriptionID *uuid.UUID      `json:"subscriptionId"`
	QuotaCharge    string          `json:"quotaCharge"`
	// ChannelTier: own | shared | platform (null: no channel attempted, or
	// logged before tiers existed). Own/shared requests have no cost.
	ChannelTier *string `json:"channelTier"`
	// ImageCount is the number of output images (image endpoints; 0 otherwise).
	ImageCount int64 `json:"imageCount"`
	// PriceMultiplier is the effective multiplier (group × schedule) of a
	// priced platform request (phase8-api.md §1.1); null otherwise.
	PriceMultiplier *string `json:"priceMultiplier"`
	// PriceTier is the aboveInputTokens of the context-length tier the sell
	// price applied (phase10-api.md §1); null = base prices or not priced.
	PriceTier *int64 `json:"priceTier"`
	// AudioSeconds is the billed duration of the input audio (audio
	// endpoints; 0 otherwise). UsageEstimated mirrors usage.estimated: the
	// usage was estimated — for audio requests, the upstream reported no
	// usage and only perRequest was charged (phase9-api.md §1.1).
	AudioSeconds   int64 `json:"audioSeconds"`
	UsageEstimated bool  `json:"usageEstimated"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r, 92)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pg := httpx.ParsePage(r)
	ctx := r.Context()
	var total int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM request_logs l`+f.sql(), f.args...).Scan(&total); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	args := append(f.args, pg.PageSize, pg.Offset())
	rows, err := h.pool.Query(ctx, `SELECT l.id, l.request_id, l.started_at, l.user_id, u.display_name, l.key_id, l.key_name, l.inbound,
		l.model, l.channel_id, l.channel_name, l.upstream_model, l.stream, l.status_code, l.error_class, l.error_message, l.attempts,
		l.fallback_path, l.ttft_ms, l.duration_ms, l.input_tokens, l.output_tokens, l.cache_read_tokens, l.cache_write_tokens,
		l.reasoning_tokens, l.usage_estimated, l.cost_nano, l.charge_nano, l.subscription_id, l.quota_charge_nano,
		COALESCE(l.served_model, l.model), l.channel_tier, l.image_count, l.image_input_tokens, l.price_multiplier,
		l.audio_seconds, l.audio_input_tokens, l.audio_output_tokens, l.input_characters, l.price_tier
		FROM request_logs l LEFT JOIN users u ON u.id = l.user_id`+f.sql()+
		` ORDER BY l.started_at DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []logView{}
	for rows.Next() {
		var v logView
		var uid *uuid.UUID
		var uname *string
		var ttft *int32
		var dur int32
		var cost, charge, quota int64
		if err := rows.Scan(&v.ID, &v.RequestID, &v.StartedAt, &uid, &uname, &v.KeyID, &v.KeyName, &v.Inbound, &v.Model, &v.ChannelID,
			&v.ChannelName, &v.UpstreamModel, &v.Stream, &v.StatusCode, &v.ErrorClass, &v.ErrorMessage, &v.Attempts, &v.FallbackPath,
			&ttft, &dur, &v.Usage.Input, &v.Usage.Output, &v.Usage.CacheRead, &v.Usage.CacheWrite, &v.Usage.Reasoning,
			&v.Usage.Estimated, &cost, &charge, &v.SubscriptionID, &quota, &v.ServedModel, &v.ChannelTier, &v.ImageCount,
			&v.Usage.ImageInputTokens, &v.PriceMultiplier, &v.AudioSeconds, &v.Usage.AudioInputTokens, &v.Usage.AudioOutputTokens,
			&v.Usage.InputCharacters, &v.PriceTier); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if uid != nil {
			v.User = &userRef{ID: *uid}
			if uname != nil {
				v.User.DisplayName = *uname
			}
		}
		if ttft != nil {
			t := int64(*ttft)
			v.TTFTMs = &t
		}
		v.DurationMs = int64(dur)
		v.UsageEstimated = v.Usage.Estimated
		v.Charge = money.Amount(charge).String()
		v.QuotaCharge = money.Amount(quota).String()
		if f.seeAll && (v.ChannelTier == nil || *v.ChannelTier == "platform") {
			c := money.Amount(cost).String()
			v.Cost = &c
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[logView]{Items: items, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

// numericString converts a NUMERIC sum of nano units (which may exceed int64)
// into a decimal string in currency units without floating point.
func numericString(s *string) string {
	if s == nil || *s == "" {
		return "0"
	}
	digits, neg := strings.TrimPrefix(*s, "-"), strings.HasPrefix(*s, "-")
	if strings.ContainsFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) {
		return "0"
	}
	for len(digits) <= money.Scale {
		digits = "0" + digits
	}
	intPart, frac := digits[:len(digits)-money.Scale], strings.TrimRight(digits[len(digits)-money.Scale:], "0")
	out := intPart
	if frac != "" {
		out += "." + frac
	}
	if neg && out != "0" {
		out = "-" + out
	}
	return out
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r, 92)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	out, err := h.buildSummary(ctx, f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) buildSummary(ctx context.Context, f *filter) (map[string]any, error) {
	where := f.sql()
	dl := h.pool.Dialect()
	var reqs, ok, errs, in, outTok int64
	var cost, charge *string
	var p50, p95, p99, ttft *float64
	err := h.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status_code < 400), count(*) FILTER (WHERE status_code >= 400),
		coalesce(sum(input_tokens + cache_read_tokens + cache_write_tokens), 0), coalesce(sum(output_tokens), 0),
		`+dl.SumText("cost_nano")+`, `+dl.SumText("charge_nano")+`,
		`+dl.Percentile(0.5, "duration_ms")+`, `+dl.Percentile(0.95, "duration_ms")+`,
		`+dl.Percentile(0.99, "duration_ms")+`, `+dl.Percentile(0.5, "ttft_ms")+`
		FROM request_logs l`+where, f.args...).Scan(&reqs, &ok, &errs, &in, &outTok, &cost, &charge, &p50, &p95, &p99, &ttft)
	if err != nil {
		return nil, err
	}
	rate := 0.0
	if reqs > 0 {
		rate = float64(ok) / float64(reqs)
	}
	round := func(v *float64) *int64 {
		if v == nil {
			return nil
		}
		n := int64(*v + 0.5)
		return &n
	}
	totals := map[string]any{
		"requests": reqs, "success": ok, "errors": errs, "successRate": rate, "inputTokens": in, "outputTokens": outTok,
		"cost": nil, "charge": numericString(charge),
		"latencyP50Ms": round(p50), "latencyP95Ms": round(p95), "latencyP99Ms": round(p99), "ttftP50Ms": round(ttft),
	}
	if f.seeAll {
		totals["cost"] = numericString(cost)
	}

	daily := []map[string]any{}
	rows, err := h.pool.Query(ctx, `SELECT `+dl.DateUTC("started_at")+` d, count(*),
		count(*) FILTER (WHERE status_code >= 400), coalesce(sum(input_tokens + cache_read_tokens + cache_write_tokens), 0),
		coalesce(sum(output_tokens), 0), `+dl.SumText("charge_nano")+`
		FROM request_logs l`+where+` GROUP BY d ORDER BY d`, f.args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d string
		var n, e, i, o int64
		var c *string
		if err := rows.Scan(&d, &n, &e, &i, &o, &c); err != nil {
			rows.Close()
			return nil, err
		}
		daily = append(daily, map[string]any{"date": d, "requests": n, "errors": e, "inputTokens": i, "outputTokens": o, "charge": numericString(c)})
	}
	rows.Close()

	byModel := []map[string]any{}
	rows, err = h.pool.Query(ctx, `SELECT model, count(*), count(*) FILTER (WHERE status_code >= 400),
		coalesce(sum(input_tokens + cache_read_tokens + cache_write_tokens), 0), coalesce(sum(output_tokens), 0), `+dl.SumText("charge_nano")+`
		FROM request_logs l`+where+` GROUP BY model ORDER BY count(*) DESC, model LIMIT 50`, f.args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m string
		var n, e, i, o int64
		var c *string
		if err := rows.Scan(&m, &n, &e, &i, &o, &c); err != nil {
			rows.Close()
			return nil, err
		}
		byModel = append(byModel, map[string]any{"model": m, "requests": n, "errors": e, "inputTokens": i, "outputTokens": o, "charge": numericString(c)})
	}
	rows.Close()

	byChannel := []map[string]any{}
	rows, err = h.pool.Query(ctx, `SELECT channel_id, max(channel_name), count(*), count(*) FILTER (WHERE status_code >= 400),
		`+dl.Percentile(0.95, "duration_ms")+`
		FROM request_logs l`+where+` GROUP BY channel_id ORDER BY count(*) DESC, max(channel_name), channel_id LIMIT 50`, f.args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id *uuid.UUID
		var name *string
		var n, e int64
		var p *float64
		if err := rows.Scan(&id, &name, &n, &e, &p); err != nil {
			rows.Close()
			return nil, err
		}
		byChannel = append(byChannel, map[string]any{"channelId": id, "channelName": name, "requests": n, "errors": e, "latencyP95Ms": round(p)})
	}
	rows.Close()
	return map[string]any{"from": f.from, "to": f.to, "totals": totals, "daily": daily, "byModel": byModel, "byChannel": byChannel}, rows.Err()
}
