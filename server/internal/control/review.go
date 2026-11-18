package control

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
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

func (s *Service) snapshotForEvent(ctx context.Context, id uuid.UUID) (Snapshot, error) {
	var snapshot Snapshot
	var payload string
	err := s.pool.QueryRow(ctx, `SELECT s.id,s.request_id,s.model,s.dialect,s.payload,s.truncated,s.created_at
		FROM control_events e JOIN control_snapshots s ON s.id=e.snapshot_id WHERE e.id=$1`, id).
		Scan(&snapshot.ID, &snapshot.RequestID, &snapshot.Model, &snapshot.Dialect, &payload, &snapshot.Truncated, &snapshot.CreatedAt)
	if db.IsNoRows(err) {
		return snapshot, apperr.NotFound("请求快照")
	}
	snapshot.Payload = json.RawMessage(payload)
	return snapshot, err
}

func (s *Service) getSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("请求快照"))
		return
	}
	snapshot, err := s.snapshotForEvent(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, snapshot)
}

func (s *Service) getReviewModels(w http.ResponseWriter, _ *http.Request) {
	models := []string{}
	if s.ReviewModels != nil {
		models = s.ReviewModels()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Service) review(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EventID uuid.UUID `json:"eventId"`
		Model   string    `json:"model"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in.Model = strings.TrimSpace(in.Model)
	if in.EventID == uuid.Nil || in.Model == "" || s.OnReview == nil || s.ReviewModels == nil || !slices.Contains(s.ReviewModels(), in.Model) {
		httpx.WriteError(w, r, apperr.Validation("请指定审查记录并选择可用的平台文本模型", nil))
		return
	}
	snapshot, err := s.snapshotForEvent(r.Context(), in.EventID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var target struct {
		RequestID string  `json:"requestId"`
		Excerpt   string  `json:"excerpt"`
		Threshold float64 `json:"threshold"`
		Category  string  `json:"jevCategory"`
	}
	if err := s.pool.QueryRow(r.Context(), `SELECT request_id,excerpt,COALESCE(review_threshold,threshold),reason FROM control_events WHERE id=$1`, in.EventID).
		Scan(&target.RequestID, &target.Excerpt, &target.Threshold, &target.Category); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	evidence, _ := json.Marshal(struct {
		RequestID string   `json:"requestId"`
		Excerpt   string   `json:"requestExcerpt"`
		Category  string   `json:"jevCategory"`
		Snapshot  Snapshot `json:"snapshot"`
	}{target.RequestID, target.Excerpt, target.Category, snapshot})
	select {
	case s.reviewSlots <- struct{}{}:
		defer func() { <-s.reviewSlots }()
	default:
		httpx.WriteError(w, r, apperr.New(apperr.KindConflict, "review_busy", "模型复核繁忙，请稍后再试"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	result, err := s.OnReview(ctx, in.Model, string(evidence))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	result = strings.TrimSpace(result)
	var assessment struct {
		Probability *float64 `json:"probability"`
		Reason      string   `json:"reason"`
	}
	// A non-JSON or inconclusive answer remains readable, but cannot be applied.
	var token string
	var effectiveHit *bool
	jsonResult := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(result, "```json"), "```"), "```"))
	hasTargetEvidence := target.RequestID == snapshot.RequestID || strings.TrimSpace(target.Excerpt) != ""
	if hasTargetEvidence && json.Unmarshal([]byte(jsonResult), &assessment) == nil && assessment.Probability != nil && *assessment.Probability >= 0 && *assessment.Probability <= 1 {
		hit := *assessment.Probability >= target.Threshold
		effectiveHit = &hit
		sealed, _ := json.Marshal(reviewDecision{Probability: *assessment.Probability, SnapshotID: snapshot.ID, ExpiresAt: time.Now().Add(15 * time.Minute)})
		token, err = s.box.Seal(sealed, "review:"+in.EventID.String()+":"+auth.PrincipalFrom(r.Context()).UserID.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	} else {
		assessment.Probability = nil
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"model": in.Model, "result": result, "snapshotId": snapshot.ID, "probability": assessment.Probability,
		"sourceRequestId": snapshot.RequestID, "threshold": target.Threshold, "effectiveHit": effectiveHit, "applyToken": token,
	})
}

type reviewDecision struct {
	Probability float64
	SnapshotID  uuid.UUID
	ExpiresAt   time.Time
}

func (s *Service) applyReview(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	if !p.Can(authz.UsersWrite) {
		httpx.WriteError(w, r, apperr.Forbidden())
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("管控记录"))
		return
	}
	var in struct {
		Token     string   `json:"token"`
		Threshold *float64 `json:"threshold"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in.Threshold != nil && (*in.Threshold < 0 || *in.Threshold > 1) {
		httpx.WriteError(w, r, apperr.Validation("复核阈值须在 0 到 1 之间", nil))
		return
	}
	now := time.Now().UTC()
	var decision reviewDecision
	raw, err := s.box.Open(in.Token, "review:"+id.String()+":"+p.UserID.String())
	if err != nil || json.Unmarshal(raw, &decision) != nil || !decision.ExpiresAt.After(now) {
		httpx.WriteError(w, r, apperr.Validation("复核结果无效或已过期，请重新复核", nil))
		return
	}
	ctx := r.Context()
	var threshold float64
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE control_events SET review_probability=$2,reviewed_at=$3,review_threshold=COALESCE($5,review_threshold,threshold)
			WHERE id=$1 AND snapshot_id=$4 RETURNING review_threshold`, id, decision.Probability, now, decision.SnapshotID, in.Threshold).Scan(&threshold)
		if db.IsNoRows(err) {
			return apperr.NotFound("管控记录")
		}
		if err != nil {
			return err
		}
		eventID := id.String()
		return s.audit.Record(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: "control.review_apply",
			ResourceType: "control_event", ResourceID: &eventID, RequestID: httpx.RequestID(ctx),
			Metadata: map[string]any{"probability": decision.Probability, "threshold": threshold, "effectiveHit": decision.Probability >= threshold}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"reviewProbability": decision.Probability, "reviewThreshold": threshold, "effectiveHit": decision.Probability >= threshold, "reviewedAt": now})
}
