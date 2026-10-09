package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/identity"
	"omnigate/internal/platform/db"
)

// Audit actions.
const (
	ActionCreate  = "route.create"
	ActionUpdate  = "route.update"
	ActionDelete  = "route.delete"
	ActionReorder = "route.reorder"
)

// CodeTargetInvalid is returned when a target channel does not exist or does
// not serve any model the rule matches.
const CodeTargetInvalid = "route_target_invalid"

// DefaultRefresh is how often other instances' changes are picked up.
const DefaultRefresh = 5 * time.Second

// Actor and RequestMeta identify who changes rules (audit).
type Actor struct {
	ID   uuid.UUID
	Name string
}

type RequestMeta struct {
	IPPrefix  string
	RequestID string
}

// Service stores rules and serves the in-memory snapshot used for matching.
type Service struct {
	pool *db.DB
	rec  *audit.Recorder
	log  *slog.Logger
	now  func() time.Time

	// Latency is the in-process TTFB average used by least_latency.
	Latency *Latency
	// Refresh is the snapshot reload interval of Run.
	Refresh time.Duration

	mu    sync.RWMutex
	rules []*Rule // all rules, by position

	rrMu sync.Mutex
	rr   map[uuid.UUID]*atomic.Uint64
}

func NewService(pool *db.DB, rec *audit.Recorder, log *slog.Logger) *Service {
	return &Service{pool: pool, rec: rec, log: log, now: func() time.Time { return time.Now().UTC() },
		Latency: NewLatency(), Refresh: DefaultRefresh, rr: map[uuid.UUID]*atomic.Uint64{}}
}

const ruleCols = `id, name, description, enabled, position, spec, version, created_at, updated_at`

func scanRule(row db.Row) (*Rule, error) {
	var r Rule
	var raw []byte
	if err := row.Scan(&r.ID, &r.Name, &r.Description, &r.Enabled, &r.Position, &raw, &r.Version, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	var s spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("route rule %s: %w", r.ID, err)
	}
	r.setSpec(s)
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	return &r, nil
}

func (s *Service) load(ctx context.Context, q db.Querier) ([]*Rule, error) {
	rows, err := q.Query(ctx, `SELECT `+ruleCols+` FROM route_rules ORDER BY position, created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reload refreshes the snapshot from the database.
func (s *Service) Reload(ctx context.Context) error {
	rules, err := s.load(ctx, s.pool)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.rules = rules
	s.mu.Unlock()
	return nil
}

// Run reloads the snapshot every Refresh until ctx is done (multi-instance).
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.Refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.Reload(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("route rules reload failed", "err", err)
		}
	}
}

// Match returns the first enabled rule (by position) matching model and role,
// or nil. The returned rule must not be modified.
func (s *Service) Match(model string, role identity.Role) *Rule {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.rules {
		if r.Matches(model, role) {
			return r
		}
	}
	return nil
}

// NextRoundRobin returns the rule's next rotation counter (in-process).
func (s *Service) NextRoundRobin(ruleID uuid.UUID) uint64 {
	s.rrMu.Lock()
	c, ok := s.rr[ruleID]
	if !ok {
		c = &atomic.Uint64{}
		s.rr[ruleID] = c
	}
	s.rrMu.Unlock()
	return c.Add(1) - 1
}

// PeekRoundRobin returns the rule's next rotation counter without advancing it.
func (s *Service) PeekRoundRobin(ruleID uuid.UUID) uint64 {
	s.rrMu.Lock()
	defer s.rrMu.Unlock()
	if c, ok := s.rr[ruleID]; ok {
		return c.Load()
	}
	return 0
}

// List returns all rules by position (from the database).
func (s *Service) List(ctx context.Context) ([]*Rule, error) {
	return s.load(ctx, s.pool)
}

func (s *Service) get(ctx context.Context, q db.Querier, id uuid.UUID, lock bool) (*Rule, error) {
	sql := `SELECT ` + ruleCols + ` FROM route_rules WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	r, err := scanRule(q.QueryRow(ctx, sql, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("路由规则")
	}
	return r, err
}

// validateTargets checks that each target channel exists and serves at least
// one model matched by the rule.
func (s *Service) validateTargets(ctx context.Context, q db.Querier, r *Rule) error {
	details := map[string]any{}
	for i, t := range r.Targets {
		field := fmt.Sprintf("targets[%d].channelId", i)
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM channels WHERE id = $1)`, t.ChannelID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			details[field] = "渠道不存在"
			continue
		}
		rows, err := q.Query(ctx, `SELECT model FROM channel_models WHERE channel_id = $1`, t.ChannelID)
		if err != nil {
			return err
		}
		models, err := db.CollectRows[string](rows)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(models, r.MatchesAnyModel) {
			details[field] = "该渠道不提供规则匹配的任何模型"
		}
	}
	if len(details) > 0 {
		return &apperr.Error{Kind: apperr.KindValidation, Code: CodeTargetInvalid, Message: "目标渠道不存在或不提供该模型", Details: details}
	}
	return nil
}

func (s *Service) audit(ctx context.Context, q db.Querier, a Actor, m RequestMeta, action string, id *uuid.UUID, md map[string]any) error {
	var rid *string
	if id != nil {
		v := id.String()
		rid = &v
	}
	return s.rec.Record(ctx, q, audit.Entry{ActorID: &a.ID, ActorName: &a.Name, Action: action, ResourceType: "route_rule",
		ResourceID: rid, IPPrefix: m.IPPrefix, RequestID: m.RequestID, Metadata: md})
}

func summary(r *Rule) map[string]any {
	return map[string]any{"name": r.Name, "enabled": r.Enabled, "match": r.Match, "targets": r.Targets, "strategy": r.Strategy,
		"protocolPreference": r.ProtocolPreference, "retry": r.Retry, "fallbackModels": r.FallbackModels}
}

func invalid(details map[string]any) error { return apperr.Validation("参数校验失败", details) }

// Create appends a rule at the end.
func (s *Service) Create(ctx context.Context, a Actor, in Input, m RequestMeta) (*Rule, error) {
	r := &Rule{Enabled: true}
	if d := in.apply(r, true); len(d) > 0 {
		return nil, invalid(d)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	now := s.now()
	r.ID, r.Version, r.CreatedAt, r.UpdatedAt = id, 1, now, now
	raw, err := json.Marshal(r.spec())
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if err := s.validateTargets(ctx, tx, r); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM route_rules`).Scan(&r.Position); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO route_rules (`+ruleCols+`, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			r.ID, r.Name, r.Description, r.Enabled, r.Position, raw, r.Version, r.CreatedAt, r.UpdatedAt, a.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, m, ActionCreate, &r.ID, summary(r))
	})
	if err != nil {
		return nil, err
	}
	s.reloadAfterWrite(ctx)
	return r, nil
}

// Update applies a partial update guarded by version.
func (s *Service) Update(ctx context.Context, a Actor, id uuid.UUID, in Input, m RequestMeta) (*Rule, error) {
	var out *Rule
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		r, err := s.get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		before := summary(r)
		if d := in.apply(r, false); len(d) > 0 {
			return invalid(d)
		}
		if *in.Version != r.Version {
			return apperr.VersionConflict()
		}
		if err := s.validateTargets(ctx, tx, r); err != nil {
			return err
		}
		raw, err := json.Marshal(r.spec())
		if err != nil {
			return err
		}
		r.Version++
		r.UpdatedAt = s.now()
		if _, err := tx.Exec(ctx, `UPDATE route_rules SET name = $2, description = $3, enabled = $4, spec = $5, version = $6, updated_at = $7
			WHERE id = $1`, r.ID, r.Name, r.Description, r.Enabled, raw, r.Version, r.UpdatedAt); err != nil {
			return err
		}
		out = r
		return s.audit(ctx, tx, a, m, ActionUpdate, &r.ID, map[string]any{"before": before, "after": summary(r)})
	})
	if err != nil {
		return nil, err
	}
	s.reloadAfterWrite(ctx)
	return out, nil
}

// Delete removes a rule and closes the gap in positions.
func (s *Service) Delete(ctx context.Context, a Actor, id uuid.UUID, m RequestMeta) error {
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		r, err := s.get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM route_rules WHERE id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE route_rules SET position = position - 1 WHERE position > $1`, r.Position); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, m, ActionDelete, &r.ID, summary(r))
	})
	if err != nil {
		return err
	}
	s.reloadAfterWrite(ctx)
	return nil
}

// Reorder sets positions from ids, which must contain every rule exactly once.
// Reordering does not change rule versions.
func (s *Service) Reorder(ctx context.Context, a Actor, ids []uuid.UUID, m RequestMeta) ([]*Rule, error) {
	var out []*Rule
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM route_rules ORDER BY position, created_at, id FOR UPDATE`)
		if err != nil {
			return err
		}
		current, err := db.CollectRows[uuid.UUID](rows)
		if err != nil {
			return err
		}
		if len(ids) != len(current) || len(dedupe(ids)) != len(ids) {
			return invalid(map[string]any{"ids": "必须恰好包含全部规则，且不能重复"})
		}
		for _, id := range ids {
			if !slices.Contains(current, id) {
				return invalid(map[string]any{"ids": "必须恰好包含全部规则，且不能重复"})
			}
		}
		now := s.now()
		for i, id := range ids {
			if _, err := tx.Exec(ctx, `UPDATE route_rules SET position = $2, updated_at = CASE WHEN position = $2 THEN updated_at ELSE $3 END
				WHERE id = $1`, id, i, now); err != nil {
				return err
			}
		}
		if err := s.audit(ctx, tx, a, m, ActionReorder, nil, map[string]any{"before": current, "after": ids}); err != nil {
			return err
		}
		out, err = s.load(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.reloadAfterWrite(ctx)
	return out, nil
}

func (s *Service) reloadAfterWrite(ctx context.Context) {
	if err := s.Reload(context.WithoutCancel(ctx)); err != nil {
		s.log.ErrorContext(ctx, "route rules reload failed", "err", err)
	}
}
