// Package audit records security-relevant actions in an append-only log.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/platform/db"
)

// Well-known actions. Keep them stable: dashboards and alerts key on them.
const (
	ActionLogin              = "auth.login"
	ActionLoginDenied        = "auth.login_denied"
	ActionLogout             = "auth.logout"
	ActionSessionRevoke      = "auth.session_revoke"
	ActionSessionRevokeOther = "auth.session_revoke_others"
	ActionUserCreate         = "user.create"
	ActionUserUpdate         = "user.update"
)

type Entry struct {
	ID           uuid.UUID      `json:"id"`
	ActorID      *uuid.UUID     `json:"actorId"`
	ActorName    *string        `json:"actorName"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resourceType"`
	ResourceID   *string        `json:"resourceId"`
	IPPrefix     string         `json:"ipPrefix"`
	RequestID    string         `json:"requestId"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    time.Time      `json:"createdAt"`
}

// metadataKey carries extra audit metadata in a context (WithMetadata).
type metadataKey struct{}

// WithMetadata returns a context whose audit entries also carry md (entries'
// own keys win). Non-HTTP callers such as `omnigate seed` use it to mark
// their changes (e.g. {"source": "seed"}) without changing service APIs.
func WithMetadata(ctx context.Context, md map[string]any) context.Context {
	merged := map[string]any{}
	if prev, ok := ctx.Value(metadataKey{}).(map[string]any); ok {
		for k, v := range prev {
			merged[k] = v
		}
	}
	for k, v := range md {
		merged[k] = v
	}
	return context.WithValue(ctx, metadataKey{}, merged)
}

type Recorder struct {
	pool *db.DB
	log  *slog.Logger
}

func NewRecorder(pool *db.DB, log *slog.Logger) *Recorder {
	return &Recorder{pool: pool, log: log}
}

// Record writes an audit entry using q (pass a tx to make it atomic with the
// change being audited, or nil to use the pool). Callers must never put secrets
// into Metadata.
func (r *Recorder) Record(ctx context.Context, q db.Querier, e Entry) error {
	if q == nil {
		q = r.pool
	}
	if e.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		e.ID = id
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
	}
	if extra, ok := ctx.Value(metadataKey{}).(map[string]any); ok && len(extra) > 0 {
		md := make(map[string]any, len(e.Metadata)+len(extra))
		for k, v := range extra {
			md[k] = v
		}
		for k, v := range e.Metadata {
			md[k] = v
		}
		e.Metadata = md
	}
	// The nil UUID is the system actor (no user, e.g. `omnigate seed`): stored
	// as NULL with only ActorName set.
	if e.ActorID != nil && *e.ActorID == uuid.Nil {
		e.ActorID = nil
	}
	meta, err := json.Marshal(e.Metadata)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO audit_logs (id, actor_id, actor_name, action, resource_type, resource_id, ip_prefix, request_id, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID, e.ActorID, e.ActorName, e.Action, e.ResourceType, e.ResourceID, e.IPPrefix, e.RequestID, meta, e.CreatedAt)
	if err != nil {
		r.log.ErrorContext(ctx, "audit write failed", "action", e.Action, "err", err)
	}
	return err
}

type ListQuery struct {
	Action  string
	ActorID *uuid.UUID
	Offset  int
	Limit   int
}

func (r *Recorder) List(ctx context.Context, q ListQuery) ([]Entry, int, error) {
	where, args := "WHERE true", []any{}
	if q.Action != "" {
		args = append(args, q.Action)
		where += " AND action = $" + strconv.Itoa(len(args))
	}
	if q.ActorID != nil {
		args = append(args, *q.ActorID)
		where += " AND actor_id = $" + strconv.Itoa(len(args))
	}
	var total int
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := r.pool.Query(ctx, `
		SELECT id, actor_id, actor_name, action, resource_type, resource_id, ip_prefix, request_id, metadata, created_at
		FROM audit_logs `+where+` ORDER BY created_at DESC, id DESC
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.Action, &e.ResourceType, &e.ResourceID,
			&e.IPPrefix, &e.RequestID, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
