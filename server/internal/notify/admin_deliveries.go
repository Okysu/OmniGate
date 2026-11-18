package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/httpx"
)

// EmailDelivery reports the outbox state. Sent means accepted by SMTP, not
// receipt by the recipient's inbox. Notification bodies and raw event data are omitted.
type EmailDelivery struct {
	ID            uuid.UUID  `json:"id"`
	UserID        uuid.UUID  `json:"userId"`
	UserName      string     `json:"userName"`
	EventID       *uuid.UUID `json:"eventId"`
	Title         string     `json:"title"`
	Type          string     `json:"type"`
	Kind          string     `json:"kind"`
	ForceEmail    bool       `json:"forceEmail"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
	SentAt        *time.Time `json:"sentAt"`
	LastError     *string    `json:"lastError"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type EmailDeliveryQuery struct {
	UserID, EventID *uuid.UUID
	Status          string
	Offset, Limit   int
}

var deliveryStatuses = []string{"pending", "sending", "sent", "failed", "skipped", "held", "merged"}

func (h *Handler) emailDeliveries(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	q := EmailDeliveryQuery{Status: r.URL.Query().Get("status"), Offset: pg.Offset(), Limit: pg.PageSize}
	for name, dst := range map[string]**uuid.UUID{"userId": &q.UserID, "eventId": &q.EventID} {
		if v := r.URL.Query().Get(name); v != "" {
			id, err := uuid.Parse(v)
			if err != nil || id == uuid.Nil {
				httpx.WriteError(w, r, apperr.Validation(name+" 必须是有效 UUID", nil))
				return
			}
			*dst = &id
		}
	}
	items, total, err := h.svc.EmailDeliveries(r.Context(), auth.PrincipalFrom(r.Context()), q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[EmailDelivery]{Items: items, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}

func (s *Service) EmailDeliveries(ctx context.Context, actor *authz.Principal, q EmailDeliveryQuery) ([]EmailDelivery, int, error) {
	if !actor.Can(authz.UsersWrite) {
		return nil, 0, apperr.Forbidden()
	}
	if q.Status != "" && !slices.Contains(deliveryStatuses, q.Status) {
		return nil, 0, apperr.Validation("无效的邮件投递状态", map[string]any{"status": strings.Join(deliveryStatuses, ", ")})
	}
	where := " WHERE d.channel = 'email'"
	args := []any{}
	add := func(column string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(" AND %s = $%d", column, len(args))
	}
	if q.UserID != nil {
		add("d.user_id", *q.UserID)
	}
	if q.EventID != nil {
		add("d.event_id", *q.EventID)
	}
	if q.Status != "" {
		add("d.status", q.Status)
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries d`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, min(max(q.Limit, 1), 200), max(q.Offset, 0))
	rows, err := s.pool.Query(ctx, `SELECT d.id, d.user_id, u.display_name, d.event_id, COALESCE(e.title, ''),
		d.type, d.kind, d.status, d.attempts, d.next_attempt_at, d.sent_at, d.last_error, d.created_at, d.updated_at, e.data
		FROM notification_deliveries d JOIN users u ON u.id = d.user_id LEFT JOIN notification_events e ON e.id = d.event_id`+where+
		fmt.Sprintf(" ORDER BY d.created_at DESC, d.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []EmailDelivery{}
	for rows.Next() {
		var item EmailDelivery
		var raw []byte
		if err := rows.Scan(&item.ID, &item.UserID, &item.UserName, &item.EventID, &item.Title, &item.Type, &item.Kind,
			&item.Status, &item.Attempts, &item.NextAttemptAt, &item.SentAt, &item.LastError, &item.CreatedAt, &item.UpdatedAt, &raw); err != nil {
			return nil, 0, err
		}
		var data map[string]any
		_ = json.Unmarshal(raw, &data)
		item.ForceEmail = forcedEmail(item.Type, data)
		if item.Status != "pending" && item.Status != "sending" {
			item.NextAttemptAt = nil
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}
