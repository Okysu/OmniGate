package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
)

func (s *Service) AdminRoutes(r chi.Router) {
	r.With(auth.Require(authz.SettingsWrite)).Get("/control/settings", s.getSettings)
	r.With(auth.Require(authz.SettingsWrite)).Put("/control/settings", s.putSettings)
	r.With(auth.Require(authz.SettingsWrite)).Get("/control/events", s.listEvents)
	r.With(auth.Require(authz.SettingsWrite)).Get("/control/events/{id}/snapshot", s.getSnapshot)
	r.With(auth.Require(authz.SettingsWrite)).Get("/control/review-models", s.getReviewModels)
	r.With(auth.Require(authz.SettingsWrite)).Post("/control/review", s.review)
	r.With(auth.Require(authz.SettingsWrite)).Post("/control/events/{id}/apply-review", s.applyReview)
	r.With(auth.Require(authz.SettingsWrite)).Delete("/control/events/{id}", s.deleteEvent)
	r.With(auth.Require(authz.SettingsWrite)).Delete("/control/events", s.deleteEvent)
}
func (s *Service) getSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Settings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, v)
}
func (s *Service) putSettings(w http.ResponseWriter, r *http.Request) {
	var in SaveInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := s.Save(r.Context(), auth.PrincipalFrom(r.Context()), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, v)
}

type eventView struct {
	Event
	EffectiveHit bool   `json:"effectiveHit"`
	UserName     string `json:"userName"`
	KeyName      string `json:"keyName"`
	ChannelName  string `json:"channelName"`
}

func (s *Service) listEvents(w http.ResponseWriter, r *http.Request) {
	pg := httpx.ParsePage(r)
	where := " WHERE 1=1"
	args := []any{}
	for _, f := range []struct{ name, col string }{{"userId", "e.user_id"}, {"channelId", "e.channel_id"}, {"ruleId", "e.rule_id"}, {"model", "e.model"}, {"requestId", "e.request_id"}} {
		if v := r.URL.Query().Get(f.name); v != "" {
			if f.name == "userId" || f.name == "channelId" || f.name == "ruleId" {
				if id, err := uuid.Parse(v); err != nil || id == uuid.Nil {
					httpx.WriteError(w, r, apperr.Validation("筛选 ID 无效", nil))
					return
				}
			}
			args = append(args, v)
			where += fmt.Sprintf(" AND %s=$%d", f.col, len(args))
		}
	}
	var total int
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM control_events e`+where, args...).Scan(&total); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	args = append(args, pg.PageSize, pg.Offset())
	rows, err := s.pool.Query(r.Context(), `SELECT e.id,e.request_id,e.rule_id,e.user_id,e.key_id,e.channel_id,e.model,e.dialect,e.reason,e.probability,e.scores,e.excerpt,e.failure,e.action,e.action_state,e.action_error,e.created_at,e.snapshot_id,e.threshold,e.review_probability,e.review_threshold,e.reviewed_at,COALESCE(u.display_name,''),COALESCE(k.name,''),COALESCE(c.name,'')
		FROM control_events e LEFT JOIN users u ON e.user_id=u.id LEFT JOIN gateway_keys k ON e.key_id=k.id LEFT JOIN channels c ON e.channel_id=c.id`+where+fmt.Sprintf(" ORDER BY e.created_at DESC,e.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []eventView{}
	for rows.Next() {
		var e eventView
		var raw string
		if err = rows.Scan(&e.ID, &e.RequestID, &e.RuleID, &e.UserID, &e.KeyID, &e.ChannelID, &e.Model, &e.Dialect, &e.Reason, &e.Probability, &raw, &e.Excerpt, &e.Failure, &e.Action, &e.ActionState, &e.ActionError, &e.CreatedAt, &e.SnapshotID, &e.Threshold, &e.ReviewProbability, &e.ReviewThreshold, &e.ReviewedAt, &e.UserName, &e.KeyName, &e.ChannelName); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		_ = json.Unmarshal([]byte(raw), &e.Scores)
		score, threshold := e.Probability, e.Threshold
		if e.ReviewProbability != nil {
			score = e.ReviewProbability
		}
		if e.ReviewThreshold != nil {
			threshold = *e.ReviewThreshold
		}
		e.EffectiveHit = score != nil && *score >= threshold
		items = append(items, e)
	}
	if err = rows.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, httpx.List[eventView]{Items: items, Total: total, Page: pg.Page, PageSize: pg.PageSize})
}
func (s *Service) deleteEvent(w http.ResponseWriter, r *http.Request) {
	where := ""
	args := []any{}
	if v := chi.URLParam(r, "id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.WriteError(w, r, apperr.NotFound("管控记录"))
			return
		}
		where = " WHERE id=$1"
		args = append(args, id)
	}
	p := auth.PrincipalFrom(r.Context())
	err := db.InTx(r.Context(), s.pool, func(tx db.Tx) error {
		tag, err := tx.Exec(r.Context(), `DELETE FROM control_events`+where, args...)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM control_snapshots WHERE NOT EXISTS (SELECT 1 FROM control_events WHERE snapshot_id=control_snapshots.id)`); err != nil {
			return err
		}
		return s.audit.Record(r.Context(), tx, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: "control.events_delete", ResourceType: "control", Metadata: map[string]any{"count": tag.RowsAffected(), "at": time.Now().UTC()}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
