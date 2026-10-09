package notify

import (
	"context"
	"html/template"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/channel"
	"omnigate/internal/platform/httpx"
)

// Alerts provides the channel part of GET /api/alerts/summary.
type Alerts interface {
	Alerts(ctx context.Context, p *authz.Principal) (channel.AlertCounts, []channel.UpstreamBalance, error)
}

// Handler serves the notification endpoints (§1, §3, §4, §5).
type Handler struct {
	svc    *Service
	alerts Alerts
}

func NewHandler(svc *Service, alerts Alerts) *Handler { return &Handler{svc: svc, alerts: alerts} }

// PublicRoutes registers the unauthenticated endpoints (mount on /api).
func (h *Handler) PublicRoutes(r chi.Router) {
	r.Get("/notifications/unsubscribe", h.unsubscribe)
}

// Routes registers the session endpoints (mount on /api inside RequireSession).
func (h *Handler) Routes(r chi.Router) {
	r.Get("/notifications", h.list)
	r.Get("/notifications/unread-count", h.unreadCount)
	r.Post("/notifications/read", h.markRead)
	r.Get("/notifications/preferences", h.getPrefs)
	r.Put("/notifications/preferences", h.putPrefs)
	r.Post("/notifications/email/verify", h.verify)
	r.Post("/notifications/email/confirm", h.confirm)
	r.Post("/notifications/webhook/test", h.testWebhook)
	r.Put("/notifications/webhook/secret", h.setSecret)
	r.Get("/alerts/summary", h.alertsSummary)
}

// AdminRoutes registers the admin endpoints (mount on /api/admin).
func (h *Handler) AdminRoutes(r chi.Router) {
	r.With(auth.Require(authz.SettingsWrite)).Post("/settings/smtp-test", h.smtpTest)
}

func meta(r *http.Request) RequestMeta {
	return RequestMeta{IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), RequestID: httpx.RequestID(r.Context())}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	pg := httpx.ParsePage(r)
	q := ListQuery{Offset: pg.Offset(), Limit: pg.PageSize}
	switch strings.ToLower(r.URL.Query().Get("unread")) {
	case "true", "1":
		q.Unread = true
	}
	for _, t := range strings.Split(r.URL.Query().Get("type"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			q.Types = append(q.Types, t)
		}
	}
	items, total, err := h.svc.List(r.Context(), p.UserID, q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[Notification]{Items: items, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

func (h *Handler) unreadCount(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.UnreadCount(r.Context(), auth.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"count": n})
}

func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []uuid.UUID `json:"ids"`
		All bool        `json:"all"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !body.All && len(body.IDs) == 0 {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", map[string]any{"ids": "需要 ids 或 all: true"}))
		return
	}
	if len(body.IDs) > 500 {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", map[string]any{"ids": "一次最多 500 条"}))
		return
	}
	if _, err := h.svc.MarkRead(r.Context(), auth.PrincipalFrom(r.Context()).UserID, body.IDs, body.All); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getPrefs(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.GetPreferences(r.Context(), auth.PrincipalFrom(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) putPrefs(w http.ResponseWriter, r *http.Request) {
	var in PreferencesInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.svc.PutPreferences(r.Context(), auth.PrincipalFrom(r.Context()), in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	exp, err := h.svc.SendVerification(r.Context(), auth.PrincipalFrom(r.Context()), body.Address)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "expiresAt": exp})
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
		Code    string `json:"code"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.svc.ConfirmVerification(r.Context(), auth.PrincipalFrom(r.Context()), body.Address, body.Code, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) testWebhook(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.TestWebhook(r.Context(), auth.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) setSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Secret *string `json:"secret"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Secret == nil {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", map[string]any{"secret": "必填（\"\" 表示清除）"}))
		return
	}
	set, err := h.svc.SetWebhookSecret(r.Context(), auth.PrincipalFrom(r.Context()), *body.Secret, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"secretSet": set})
}

func (h *Handler) smtpTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To string `json:"to"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ok, msg, err := h.svc.SMTPTest(r.Context(), body.To)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := map[string]any{"ok": ok}
	if !ok {
		out["error"] = msg
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) alertsSummary(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	counts, balances, err := h.alerts.Alerts(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	recent, _, err := h.svc.List(r.Context(), p.UserID, ListQuery{Types: ChannelAlertTypes(), Limit: 10})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"channels": counts, "balances": balances, "recent": recent})
}

var unsubPage = template.Must(template.New("u").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>{{.Title}}</title></head>
<body style="font-family:-apple-system,'PingFang SC','Microsoft YaHei',Helvetica,Arial,sans-serif;max-width:520px;margin:64px auto;padding:0 16px;color:#1f2328">
<h1 style="font-size:20px">{{.Title}}</h1><p style="line-height:1.7">{{.Message}}</p>
<p><a href="{{.Settings}}" style="color:#0969da">管理通知设置</a></p></body></html>`))

func (h *Handler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.Unsubscribe(r.Context(), r.URL.Query().Get("token"))
	wantJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	if wantJSON {
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "type": t})
		return
	}
	v := struct{ Title, Message, Settings string }{Title: "已退订", Settings: h.svc.settingsURL()}
	status := http.StatusOK
	switch {
	case err != nil && apperr.As(err).Kind == apperr.KindValidation:
		v.Title, v.Message, status = "退订链接无效", "链接无效或已损坏，请登录后在通知设置中修改。", http.StatusBadRequest
	case err != nil:
		v.Title, v.Message, status = "退订失败", "服务器暂时无法处理，请稍后重试。", http.StatusInternalServerError
	case t == "":
		v.Message = "已关闭全部邮件通知。你可以随时在通知设置中重新开启。"
	default:
		v.Message = "已关闭「" + EventLabel(t) + "」的邮件通知。你可以随时在通知设置中重新开启。"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = unsubPage.Execute(w, v)
}

// EventLabel is the Chinese name of an event type.
func EventLabel(t string) string {
	if l, ok := labels[t]; ok {
		return l
	}
	return t
}

var labels = map[string]string{
	TypeWalletBalanceLow: "钱包余额不足", TypeWalletCredited: "钱包入账", TypeSubscriptionExpiring: "订阅即将到期",
	TypeSubscriptionExpired: "订阅已结束", TypeQuotaNearLimit: "套餐额度即将用完", TypeQuotaExhausted: "套餐额度已用完",
	TypeModelPriceChanged: "模型价格变化", TypeModelRemoved: "模型不再可用", TypeModelAdded: "新模型上架",
	TypeKeyExpiring: "API Key 即将到期", TypeChannelUnhealthy: "渠道异常", TypeChannelRecovered: "渠道恢复",
	TypeChannelAuthFailed: "渠道凭据失效", TypeUpstreamBalanceLow: "上游余额不足", TypePluginPendingApproval: "插件待审批",
	TypeAccountGroupChanged: "用户组变更", TypeLimitSpendNear: "消费限额即将用完", TypeLimitSpendReached: "消费限额已用完",
}
