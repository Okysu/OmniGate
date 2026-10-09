// Package adminapi serves control-plane management endpoints under /api/admin:
// users (list with group filter, detail, role / status changes with suspension
// reason and end, forced sign-out, key suspension, batch operations including
// set_group, the auto-enable job) and the audit log. Groups themselves are
// served by internal/usergroup.
package adminapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	gwkeys "omnigate/internal/keys"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/subscription"
	"omnigate/internal/usergroup"
)

// Audit actions written by this package (user.update is audit.ActionUserUpdate).
const (
	ActionUserLogout      = "user.logout"
	ActionUserKeysDisable = "user.keys_disable"
	ActionUserBatch       = "user.batch"
	ActionUserAutoEnable  = "user.auto_enable"
)

// Error codes of user operations (phase7-api.md §4.3).
const (
	CodeCannotDisableSelf = "cannot_disable_self"
	CodeLastAdmin         = "last_admin"
	CodeNotFound          = "not_found"
)

const (
	maxReasonLen        = 200
	maxBatchIDs         = 200
	detailSubs          = 20
	revokeReasonLogout  = "admin_logout"
	revokeReasonDisable = "admin_disabled"
)

type Handler struct {
	users *identity.Store
	audit *audit.Recorder
	// OnUserChanged runs after a user's role or status changes (e.g. to drop
	// cached gateway-key authentications so disabling takes effect at once).
	OnUserChanged func()

	// Detail sources (GET /users/{id}); set by the app.
	Pool          *db.DB
	Keys          *gwkeys.Service
	Subscriptions *subscription.Service
	// SessionIdle is the session idle timeout (active session count).
	SessionIdle time.Duration
	// OnStatusChanged receives committed disables, enables and forced
	// sign-outs (notification account.status_changed). It must not block.
	OnStatusChanged func(ctx context.Context, c identity.StatusChange)
	Log             *slog.Logger
	// Groups moves users between groups (batch set_group, phase8-api.md §1.3).
	Groups *usergroup.Service

	now func() time.Time
}

func New(users *identity.Store, rec *audit.Recorder) *Handler {
	return &Handler{users: users, audit: rec, now: nowUTC, Log: slog.Default()}
}

// Users returns the user store.
func (h *Handler) Users() *identity.Store { return h.users }

func (h *Handler) Routes(r chi.Router) {
	r.With(auth.Require(authz.UsersRead)).Get("/users", h.listUsers)
	r.With(auth.Require(authz.UsersWrite)).Post("/users/batch", h.batch)
	r.With(auth.Require(authz.UsersRead)).Get("/users/{id}", h.getUser)
	r.With(auth.Require(authz.UsersWrite)).Patch("/users/{id}", h.patchUser)
	r.With(auth.Require(authz.UsersWrite)).Post("/users/{id}/logout", h.logoutUser)
	r.With(auth.Require(authz.UsersWrite)).Post("/users/{id}/keys/disable", h.disableKeys)
	r.With(auth.Require(authz.AuditRead)).Get("/audit-logs", h.listAudit)
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	q := identity.ListUsersQuery{Q: r.URL.Query().Get("q"), Sort: r.URL.Query().Get("sort"), Offset: pg.Offset(), Limit: pg.PageSize}
	if v := r.URL.Query().Get("groupId"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.WriteError(w, r, apperr.Validation("groupId 不是合法的 UUID", nil))
			return
		}
		q.GroupID = &id
	}
	users, total, err := h.users.List(r.Context(), q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[*identity.User]{Items: users, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

// ---- shared rules ----

type meta struct {
	actor     *authz.Principal
	ipPrefix  string
	requestID string
}

func metaOf(r *http.Request) meta {
	return meta{actor: auth.PrincipalFrom(r.Context()), ipPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), requestID: httpx.RequestID(r.Context())}
}

func (h *Handler) record(ctx context.Context, m meta, action string, userID *uuid.UUID, md map[string]any) {
	e := audit.Entry{Action: action, ResourceType: "user", IPPrefix: m.ipPrefix, RequestID: m.requestID, Metadata: md}
	if m.actor != nil {
		e.ActorID, e.ActorName = &m.actor.UserID, &m.actor.Name
	}
	if userID != nil {
		id := userID.String()
		e.ResourceID = &id
	}
	_ = h.audit.Record(ctx, nil, e)
}

func cannotDisableSelf(msg string) *apperr.Error {
	return apperr.New(apperr.KindConflict, CodeCannotDisableSelf, msg)
}

// suspension validates a disable reason and end (field names differ between
// PATCH and batch bodies).
func (h *Handler) suspension(reason *string, until *time.Time, reasonField, untilField string, details map[string]any) string {
	r := ""
	if reason != nil {
		r = strings.TrimSpace(*reason)
	}
	switch n := len([]rune(r)); {
	case n == 0:
		details[reasonField] = "停用时必须填写原因"
	case n > maxReasonLen:
		details[reasonField] = "不能超过 200 个字符"
	}
	if until != nil && !until.After(h.now()) {
		details[untilField] = "到期时间必须晚于现在"
	}
	return r
}

func userAudit(u *identity.User) map[string]any {
	return map[string]any{"role": u.Role, "status": u.Status, "disabledReason": u.DisabledReason, "disabledUntil": u.DisabledUntil}
}

// applyStatus runs after a committed role / status change: sessions of a
// disabled user are revoked, cached key authentications dropped, the change
// audited and the user notified when the status changed.
func (h *Handler) applyStatus(ctx context.Context, m meta, before, after *identity.User) {
	now := h.now()
	if after.Status == identity.StatusDisabled {
		if _, err := h.users.RevokeOtherSessions(ctx, after.ID, uuid.Nil, revokeReasonDisable, now); err != nil {
			h.Log.ErrorContext(ctx, "revoke sessions of disabled user", "user_id", after.ID, "err", err)
		}
	}
	if h.OnUserChanged != nil {
		h.OnUserChanged()
	}
	h.record(ctx, m, audit.ActionUserUpdate, &after.ID, map[string]any{"before": userAudit(before), "after": userAudit(after)})
	if before.Status != after.Status && h.OnStatusChanged != nil {
		c := identity.StatusChange{UserID: after.ID, Action: identity.ChangeEnabled, At: now}
		if after.Status == identity.StatusDisabled {
			c.Action, c.Reason, c.Until = identity.ChangeDisabled, after.DisabledReason, after.DisabledUntil
		}
		h.OnStatusChanged(ctx, c)
	}
}

// logout revokes every session of userID and reports the forced sign-out.
func (h *Handler) logout(ctx context.Context, m meta, userID uuid.UUID) (int64, error) {
	if m.actor != nil && userID == m.actor.UserID {
		return 0, cannotDisableSelf("不能强制下线自己")
	}
	if _, err := h.users.Get(ctx, userID); err != nil {
		return 0, err
	}
	now := h.now()
	n, err := h.users.RevokeOtherSessions(ctx, userID, uuid.Nil, revokeReasonLogout, now)
	if err != nil {
		return 0, err
	}
	h.record(ctx, m, ActionUserLogout, &userID, map[string]any{"revoked": n})
	if h.OnStatusChanged != nil {
		h.OnStatusChanged(ctx, identity.StatusChange{UserID: userID, Action: identity.ChangeLogout, At: now})
	}
	return n, nil
}

// ---- PATCH /users/{id} ----

type patchUserBody struct {
	Role           *identity.Role   `json:"role"`
	Status         *identity.Status `json:"status"`
	DisabledReason *string          `json:"disabledReason"`
	DisabledUntil  *time.Time       `json:"disabledUntil"`
	Version        *int             `json:"version"`
}

func (h *Handler) patchUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("用户"))
		return
	}
	var body patchUserBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	if body.Version == nil {
		details["version"] = "必填"
	}
	if body.Role == nil && body.Status == nil {
		details["role"] = "role 与 status 至少提供一个"
	}
	if body.Role != nil && !body.Role.Valid() {
		details["role"] = "无效的角色"
	}
	if body.Status != nil && !body.Status.Valid() {
		details["status"] = "无效的状态"
	}
	patch := identity.UserPatch{Role: body.Role, Status: body.Status}
	if body.Status != nil && *body.Status == identity.StatusDisabled {
		patch.DisabledReason = h.suspension(body.DisabledReason, body.DisabledUntil, "disabledReason", "disabledUntil", details)
		patch.DisabledUntil = utcPtr(body.DisabledUntil)
	} else if (body.DisabledReason != nil && *body.DisabledReason != "") || body.DisabledUntil != nil {
		details["disabledReason"] = "只能在停用用户（status: disabled）时设置停用原因与期限"
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", details))
		return
	}
	patch.Version = *body.Version
	m := metaOf(r)
	if id == m.actor.UserID && body.Status != nil && *body.Status == identity.StatusDisabled {
		httpx.WriteError(w, r, cannotDisableSelf("不能停用自己的账号"))
		return
	}
	before, after, err := h.users.Patch(r.Context(), id, patch, h.now())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// Disabling takes effect immediately: sessions are revoked and
	// gateway-key caches flushed (the key owner's status is read on lookup).
	h.applyStatus(r.Context(), m, before, after)
	httpx.WriteJSON(w, http.StatusOK, after)
}

// ---- POST /users/{id}/logout, /users/{id}/keys/disable ----

func (h *Handler) userParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("用户"))
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) logoutUser(w http.ResponseWriter, r *http.Request) {
	id, ok := h.userParam(w, r)
	if !ok {
		return
	}
	n, err := h.logout(r.Context(), metaOf(r), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}

func (h *Handler) disableKeys(w http.ResponseWriter, r *http.Request) {
	id, ok := h.userParam(w, r)
	if !ok {
		return
	}
	if _, err := h.users.Get(r.Context(), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	n, err := h.Keys.DisableAllForUser(r.Context(), h.Pool, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.record(r.Context(), metaOf(r), ActionUserKeysDisable, &id, map[string]any{"disabled": n})
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"disabled": n})
}

// ---- POST /users/batch ----

type batchBody struct {
	IDs    []string   `json:"ids"`
	Action string     `json:"action"`
	Reason *string    `json:"reason"`
	Until  *time.Time `json:"until"`
	// GroupID is the target group of set_group (phase8-api.md §1.3).
	GroupID *string `json:"groupId"`
}

// BatchFailure is one user a batch could not change.
type BatchFailure struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BatchResult is the batch response (§2.3).
type BatchResult struct {
	Succeeded []string       `json:"succeeded"`
	Failed    []BatchFailure `json:"failed"`
}

func (h *Handler) batch(w http.ResponseWriter, r *http.Request) {
	var body batchBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	details := map[string]any{}
	if len(body.IDs) < 1 || len(body.IDs) > maxBatchIDs {
		details["ids"] = "必须有 1 到 200 个用户 ID"
	}
	var reason string
	var groupID uuid.UUID
	switch body.Action {
	case "disable":
		reason = h.suspension(body.Reason, body.Until, "reason", "until", details)
	case "enable", "logout":
	case "set_group":
		if body.GroupID == nil || *body.GroupID == "" {
			details["groupId"] = "set_group 必须指定 groupId"
		} else if id, err := uuid.Parse(*body.GroupID); err != nil {
			details["groupId"] = "用户组不存在"
		} else if !h.groupExists(r.Context(), id) {
			details["groupId"] = "用户组不存在"
		} else {
			groupID = id
		}
	default:
		details["action"] = "只能是 disable、enable、logout 或 set_group"
	}
	if len(details) > 0 {
		httpx.WriteError(w, r, apperr.Validation("参数校验失败", details))
		return
	}
	ctx, m := r.Context(), metaOf(r)
	res := BatchResult{Succeeded: []string{}, Failed: []BatchFailure{}}
	seen := map[string]bool{}
	for _, raw := range body.IDs {
		if seen[raw] {
			continue
		}
		seen[raw] = true
		id, err := uuid.Parse(raw)
		if err != nil {
			res.Failed = append(res.Failed, BatchFailure{ID: raw, Code: CodeNotFound, Message: "用户不存在"})
			continue
		}
		if err := h.batchOne(ctx, m, id, body.Action, reason, utcPtr(body.Until), groupID); err != nil {
			e := apperr.As(err)
			if e.Kind == apperr.KindInternal {
				h.Log.ErrorContext(ctx, "user batch item failed", "user_id", id, "err", err)
			}
			res.Failed = append(res.Failed, BatchFailure{ID: raw, Code: e.Code, Message: e.Message})
			continue
		}
		res.Succeeded = append(res.Succeeded, raw)
	}
	md := map[string]any{"action": body.Action, "ids": body.IDs, "reason": reason,
		"until": utcPtr(body.Until), "succeeded": res.Succeeded, "failed": res.Failed}
	if body.Action == "set_group" {
		md["groupId"] = groupID
	}
	h.record(ctx, m, ActionUserBatch, nil, md)
	httpx.WriteJSON(w, http.StatusOK, res)
}

// groupExists reports whether group id exists.
func (h *Handler) groupExists(ctx context.Context, id uuid.UUID) bool {
	var ok bool
	_ = h.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_groups WHERE id = $1)`, id).Scan(&ok)
	return ok
}

// batchOne applies action to one user with the single-user rules.
func (h *Handler) batchOne(ctx context.Context, m meta, id uuid.UUID, action, reason string, until *time.Time, groupID uuid.UUID) error {
	switch action {
	case "set_group":
		_, err := h.Groups.SetUserGroup(ctx, m.actor, id, groupID, usergroup.Meta{IPPrefix: m.ipPrefix, RequestID: m.requestID})
		return err
	case "logout":
		_, err := h.logout(ctx, m, id)
		return err
	case "disable":
		if id == m.actor.UserID {
			return cannotDisableSelf("不能停用自己的账号")
		}
		st := identity.StatusDisabled
		before, after, err := h.users.Patch(ctx, id, identity.UserPatch{Status: &st, AnyVersion: true, DisabledReason: reason, DisabledUntil: until}, h.now())
		if err != nil {
			return err
		}
		h.applyStatus(ctx, m, before, after)
		return nil
	default: // enable
		u, err := h.users.Get(ctx, id)
		if err != nil {
			return err
		}
		if u.Status == identity.StatusActive {
			return nil // nothing to change
		}
		st := identity.StatusActive
		before, after, err := h.users.Patch(ctx, id, identity.UserPatch{Status: &st, AnyVersion: true}, h.now())
		if err != nil {
			return err
		}
		h.applyStatus(ctx, m, before, after)
		return nil
	}
}

// ---- GET /users/{id} ----

// Detail is the admin user detail (§2.2).
type Detail struct {
	User          *identity.User                   `json:"user"`
	Identities    []identity.IdentityDetail        `json:"identities"`
	Wallet        *WalletSummary                   `json:"wallet"`
	Subscriptions []*subscription.SubscriptionView `json:"subscriptions"`
	Keys          []gwkeys.Summary                 `json:"keys"`
	Sessions      struct {
		Active int `json:"active"`
	} `json:"sessions"`
	Usage30d struct {
		Requests int64  `json:"requests"`
		Charge   string `json:"charge"`
		Tokens   int64  `json:"tokens"`
	} `json:"usage30d"`
	Channels struct {
		Own int `json:"own"`
	} `json:"channels"`
}

// WalletSummary is the wallet part of the detail (null when none exists).
type WalletSummary struct {
	Balance  string `json:"balance"`
	Reserved string `json:"reserved"`
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	id, ok := h.userParam(w, r)
	if !ok {
		return
	}
	d, err := h.detail(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

func (h *Handler) detail(ctx context.Context, id uuid.UUID) (*Detail, error) {
	u, err := h.users.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	now := h.now()
	d := &Detail{User: u}
	if d.Identities, err = h.users.IdentityDetails(ctx, id); err != nil {
		return nil, err
	}
	var bal, res int64
	switch err := h.Pool.QueryRow(ctx, `SELECT balance_nano, reserved_nano FROM wallets WHERE user_id = $1`, id).Scan(&bal, &res); {
	case err == nil:
		d.Wallet = &WalletSummary{Balance: money.Amount(bal).String(), Reserved: money.Amount(res).String()}
	case !db.IsNoRows(err):
		return nil, err
	}
	if d.Subscriptions, err = h.Subscriptions.Recent(ctx, id, detailSubs); err != nil {
		return nil, err
	}
	if d.Keys, err = h.Keys.ListForUser(ctx, id); err != nil {
		return nil, err
	}
	if d.Sessions.Active, err = h.users.CountActiveSessions(ctx, id, now, h.SessionIdle); err != nil {
		return nil, err
	}
	var charge int64
	if err := h.Pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(charge_nano), 0)::bigint,
			COALESCE(sum(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), 0)::bigint
		FROM request_logs WHERE user_id = $1 AND started_at >= $2`, id, now.AddDate(0, 0, -30)).
		Scan(&d.Usage30d.Requests, &charge, &d.Usage30d.Tokens); err != nil {
		return nil, err
	}
	d.Usage30d.Charge = money.Amount(charge).String()
	if err := h.Pool.QueryRow(ctx, `SELECT count(*) FROM channels WHERE owner_id = $1`, id).Scan(&d.Channels.Own); err != nil {
		return nil, err
	}
	return d, nil
}

// ---- auto-enable job (§2.1) ----

// EnableDue enables users whose suspension has ended (audit user.auto_enable,
// notification account.status_changed). It returns how many were enabled.
func (h *Handler) EnableDue(ctx context.Context) (int, error) {
	now := h.now()
	users, err := h.users.EnableDue(ctx, now)
	if err != nil || len(users) == 0 {
		return 0, err
	}
	if h.OnUserChanged != nil {
		h.OnUserChanged()
	}
	for _, u := range users {
		id := u.ID
		h.record(ctx, meta{}, ActionUserAutoEnable, &id, map[string]any{"disabledReason": u.DisabledReason, "disabledUntil": u.DisabledUntil})
		if h.OnStatusChanged != nil {
			h.OnStatusChanged(ctx, identity.StatusChange{UserID: u.ID, Action: identity.ChangeEnabled, Auto: true, At: now})
		}
	}
	return len(users), nil
}

// RunAutoEnable runs EnableDue every interval until ctx is done.
func (h *Handler) RunAutoEnable(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := h.EnableDue(ctx); err != nil && ctx.Err() == nil {
			h.Log.Error("auto-enable users failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- audit log ----

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	q := audit.ListQuery{Action: r.URL.Query().Get("action"), Offset: pg.Offset(), Limit: pg.PageSize}
	if v := r.URL.Query().Get("actorId"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.WriteError(w, r, apperr.Validation("actorId 不是合法的 UUID", nil))
			return
		}
		q.ActorID = &id
	}
	items, total, err := h.audit.List(r.Context(), q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[audit.Entry]{Items: items, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

func nowUTC() time.Time { return time.Now().UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
