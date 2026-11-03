package notify

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
)

// MessageTarget is the union of explicit users and members of selected groups.
// An empty target never means everyone.
type MessageTarget struct {
	UserIDs  []uuid.UUID `json:"userIds"`
	GroupIDs []uuid.UUID `json:"groupIds"`
}

type SendMessageInput struct {
	Target         MessageTarget `json:"target"`
	Title          string        `json:"title"`
	Body           string        `json:"body"`
	Link           string        `json:"link"`
	Severity       string        `json:"severity"`
	IdempotencyKey uuid.UUID     `json:"idempotencyKey"`
}

type SendMessageResult struct {
	Recipients  int  `json:"recipients"`
	AlreadySent bool `json:"alreadySent"`
}

func (h *Handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	var in SendMessageInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	dry := r.URL.Query().Get("dryRun") == "true" || r.URL.Query().Get("dryRun") == "1"
	res, err := h.svc.SendMessage(r.Context(), auth.PrincipalFrom(r.Context()), in, dry, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func validateMessage(in *SendMessageInput) error {
	details := map[string]any{}
	if len(in.Target.UserIDs) > 1000 || len(in.Target.GroupIDs) > 1000 {
		details["target"] = "用户或用户组列表最多 1000 项"
	}
	for _, id := range append(append([]uuid.UUID{}, in.Target.UserIDs...), in.Target.GroupIDs...) {
		if id == uuid.Nil {
			details["target"] = "不能包含空 ID"
		}
	}
	in.Target.UserIDs, in.Target.GroupIDs = dedupeIDs(in.Target.UserIDs), dedupeIDs(in.Target.GroupIDs)
	if len(in.Target.UserIDs)+len(in.Target.GroupIDs) == 0 {
		details["target"] = "请指定用户或用户组"
	}
	in.Title, in.Body, in.Link = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body), strings.TrimSpace(in.Link)
	if n := utf8.RuneCountInString(in.Title); n < 1 || n > 200 {
		details["title"] = "标题需要 1–200 个字符"
	}
	if n := utf8.RuneCountInString(in.Body); n < 1 || n > 10000 {
		details["body"] = "正文需要 1–10000 个字符"
	}
	if in.Severity == "" {
		in.Severity = SeverityInfo
	}
	if in.Severity != SeverityInfo && in.Severity != SeverityWarn && in.Severity != SeverityCritical {
		details["severity"] = "无效的通知级别"
	}
	if in.IdempotencyKey == uuid.Nil {
		details["idempotencyKey"] = "请提供本次发送的 UUID，重试时保持不变"
	}
	if in.Link != "" {
		u, err := url.ParseRequestURI(in.Link)
		if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(in.Link, "/") || strings.HasPrefix(in.Link, "//") || strings.Contains(in.Link, "\\") || len(in.Link) > 1000 {
			details["link"] = "只能填写以 / 开头的站内路径"
		}
	}
	if len(details) > 0 {
		return apperr.Validation("通知参数校验失败", details)
	}
	return nil
}

func (s *Service) messageRecipients(ctx context.Context, target MessageTarget) ([]uuid.UUID, error) {
	for _, check := range []struct {
		table, label string
		ids          []uuid.UUID
	}{
		{"users", "用户", target.UserIDs}, {"user_groups", "用户组", target.GroupIDs},
	} {
		if len(check.ids) == 0 {
			continue
		}
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+check.table+` WHERE id = ANY($1)`, check.ids).Scan(&n); err != nil {
			return nil, err
		}
		if n != len(check.ids) {
			return nil, apperr.NotFound(check.label)
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM users WHERE status = 'active' AND (id = ANY($1) OR group_id = ANY($2)) ORDER BY id LIMIT 10001`, target.UserIDs, target.GroupIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 10000 {
		return nil, apperr.Validation("单次最多发送给 10000 位用户", nil)
	}
	return ids, nil
}

func (s *Service) previousMessage(ctx context.Context, key, fingerprint string) (*SendMessageResult, error) {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT data FROM notification_events WHERE dedupe_key = $1`, key).Scan(&raw); err != nil {
		if db.IsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	var data struct {
		Fingerprint string `json:"fingerprint"`
		Recipients  int    `json:"recipients"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if data.Fingerprint != fingerprint {
		return nil, apperr.Validation("同一发送标识不能用于不同的通知；请重新打开发送窗口", nil)
	}
	return &SendMessageResult{Recipients: data.Recipients, AlreadySent: true}, nil
}

// SendMessage sends a personal in-app notification, with outbound delivery only
// if the recipient explicitly enabled it. Fan-out and audit commit atomically.
func (s *Service) SendMessage(ctx context.Context, actor *authz.Principal, in SendMessageInput, dry bool, meta RequestMeta) (*SendMessageResult, error) {
	if !actor.Can(authz.UsersWrite) {
		return nil, apperr.Forbidden()
	}
	if err := validateMessage(&in); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(in)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(raw))
	key := TypeAdminMessage + ":" + actor.UserID.String() + ":" + in.IdempotencyKey.String()
	if prior, err := s.previousMessage(ctx, key, fingerprint); err != nil || prior != nil {
		return prior, err
	}
	ids, err := s.messageRecipients(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	res := &SendMessageResult{Recipients: len(ids)}
	if dry {
		return res, nil
	}
	if len(ids) == 0 {
		return nil, apperr.Validation("没有符合条件的启用用户", map[string]any{"target": "已停用的用户不会收到通知"})
	}
	ev := Event{Type: TypeAdminMessage, Key: key, Title: in.Title, Body: in.Body, Link: in.Link, Severity: in.Severity, Users: ids,
		Data: map[string]any{"fingerprint": fingerprint, "recipients": len(ids)}}
	sent, err := s.emit(ctx, ev, func(tx db.Tx, recipients []uuid.UUID) error {
		res.Recipients = len(recipients)
		data, _ := json.Marshal(map[string]any{"fingerprint": fingerprint, "recipients": len(recipients)})
		if _, err := tx.Exec(ctx, `UPDATE notification_events SET data = $2 WHERE dedupe_key = $1`, key, data); err != nil {
			return err
		}
		listed := recipients
		if len(listed) > 500 {
			listed = listed[:500]
		}
		return s.audit.Record(ctx, tx, audit.Entry{ActorID: &actor.UserID, ActorName: &actor.Name, Action: "notification.send", ResourceType: "notification", IPPrefix: meta.IPPrefix, RequestID: meta.RequestID,
			Metadata: map[string]any{"target": in.Target, "title": in.Title, "severity": in.Severity, "idempotencyKey": in.IdempotencyKey, "recipients": len(recipients), "userIds": listed}})
	})
	if err != nil {
		return nil, err
	}
	if !sent {
		// Another request may have committed after the initial lookup. Check the
		// stored payload again so concurrent retries cannot hide a key conflict.
		return s.previousMessage(ctx, key, fingerprint)
	}
	return res, nil
}
