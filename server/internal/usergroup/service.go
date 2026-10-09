package usergroup

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/tzcache"
	"omnigate/internal/pricing"
)

// cacheTTL bounds how stale another instance's group changes can be.
const cacheTTL = 10 * time.Second

// Service manages groups and memberships. Groups are cached in memory for
// the data plane (refreshed every 10 s and after every change).
type Service struct {
	pool  *db.DB
	audit *audit.Recorder
	log   *slog.Logger
	now   func() time.Time

	mu      sync.RWMutex
	byID    map[uuid.UUID]*Group
	def     *Group
	fetched time.Time

	// OnMembership runs after users changed group or group sharing may have
	// changed (cached key authentications and the channel registry).
	OnMembership func()
	// OnUserMoved receives every committed move of a user (notification
	// account.group_changed). It must not block.
	OnUserMoved func(ctx context.Context, c Change)
}

// NewService creates the group service.
func NewService(pool *db.DB, rec *audit.Recorder, log *slog.Logger) *Service {
	return &Service{pool: pool, audit: rec, log: log, now: func() time.Time { return time.Now().UTC() }}
}

// Meta carries request context for audit entries.
type Meta struct {
	IPPrefix  string
	RequestID string
}

const groupCols = `id, name, description, price_multiplier_nano, rpm, rpd, daily_spend_nano, monthly_spend_nano, timezone,
	is_default, version, created_at, updated_at`

func scanGroup(row db.Row) (*Group, error) {
	var g Group
	var mult int64
	var daily, monthly *int64
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &mult, &g.Limits.RPM, &g.Limits.RPD, &daily, &monthly, &g.Timezone,
		&g.IsDefault, &g.Version, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	g.Multiplier = pricing.Multiplier(mult)
	if daily != nil {
		a := money.Amount(*daily)
		g.Limits.DailySpend = &a
	}
	if monthly != nil {
		a := money.Amount(*monthly)
		g.Limits.MonthlySpend = &a
	}
	return &g, nil
}

func nanoPtr(a *money.Amount) *int64 {
	if a == nil {
		return nil
	}
	v := int64(*a)
	return &v
}

// Invalidate drops the cache (the next lookup reloads).
func (s *Service) Invalidate() {
	s.mu.Lock()
	s.fetched = time.Time{}
	s.mu.Unlock()
}

func (s *Service) reload(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT `+groupCols+` FROM user_groups`)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[uuid.UUID]*Group{}
	var def *Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return err
		}
		byID[g.ID] = g
		if g.IsDefault {
			def = g
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if def == nil { // never happens after migration 00012; stay usable
		def = &Group{Name: "默认", Multiplier: pricing.One, Timezone: tzcache.Default, IsDefault: true}
	}
	s.mu.Lock()
	s.byID, s.def, s.fetched = byID, def, time.Now()
	s.mu.Unlock()
	return nil
}

// Get returns group id from the cache; an unknown or nil id yields the
// default group. The returned group must not be modified.
func (s *Service) Get(ctx context.Context, id *uuid.UUID) (*Group, error) {
	s.mu.RLock()
	fresh := s.byID != nil && time.Since(s.fetched) < cacheTTL
	s.mu.RUnlock()
	if !fresh {
		if err := s.reload(ctx); err != nil {
			return nil, err
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id != nil {
		if g, ok := s.byID[*id]; ok {
			return g, nil
		}
	}
	return s.def, nil
}

// ForUser returns userID's group.
func (s *Service) ForUser(ctx context.Context, userID uuid.UUID) (*Group, error) {
	var gid *uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT group_id FROM users WHERE id = $1`, userID).Scan(&gid)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("用户")
	}
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, gid)
}

// List returns every group with its member count, the default first, then by name.
func (s *Service) List(ctx context.Context) ([]*Group, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+groupCols+` FROM user_groups`)
	if err != nil {
		return nil, err
	}
	out, err := collect(rows)
	if err != nil {
		return nil, err
	}
	counts, err := s.memberCounts(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	for _, g := range out {
		g.Members = counts[g.ID]
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func collect(rows db.Rows) ([]*Group, error) {
	defer rows.Close()
	out := []*Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Service) memberCounts(ctx context.Context, q db.Querier) (map[uuid.UUID]int, error) {
	rows, err := q.Query(ctx, `SELECT group_id, count(*) FROM users WHERE group_id IS NOT NULL GROUP BY group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]int{}
	for rows.Next() {
		var id uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Service) members(ctx context.Context, q db.Querier, id uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM users WHERE group_id = $1`, id).Scan(&n)
	return n, err
}

func (s *Service) record(ctx context.Context, q db.Querier, p *authz.Principal, m Meta, action, resType, resID string, md map[string]any) error {
	e := audit.Entry{Action: action, ResourceType: resType, ResourceID: &resID, IPPrefix: m.IPPrefix, RequestID: m.RequestID, Metadata: md}
	if p != nil {
		e.ActorID, e.ActorName = &p.UserID, &p.Name
	}
	return s.audit.Record(ctx, q, e)
}

func nameTaken() error {
	return apperr.New(apperr.KindConflict, CodeNameExists, "用户组名称已存在")
}

// clearDefault unsets the current default group (other than keep).
func clearDefault(ctx context.Context, tx db.Tx, keep uuid.UUID, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE user_groups SET is_default = false, version = version + 1, updated_at = $2
		WHERE is_default AND id <> $1`, keep, now)
	return err
}

// Create adds a group (audit group.create). isDefault: true makes it the default.
func (s *Service) Create(ctx context.Context, p *authz.Principal, in Input, m Meta) (*Group, error) {
	now := s.now()
	g := &Group{Multiplier: pricing.One, Timezone: tzcache.Default, Version: 1, CreatedAt: now, UpdatedAt: now}
	details := map[string]any{}
	in.apply(g, true, details)
	if len(details) > 0 {
		return nil, apperr.Validation("用户组参数校验失败", details)
	}
	g.IsDefault = in.IsDefault != nil && *in.IsDefault
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	g.ID = id
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if g.IsDefault {
			if err := clearDefault(ctx, tx, g.ID, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_groups (`+groupCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, $11, $11)`,
			g.ID, g.Name, g.Description, int64(g.Multiplier), g.Limits.RPM, g.Limits.RPD, nanoPtr(g.Limits.DailySpend),
			nanoPtr(g.Limits.MonthlySpend), g.Timezone, g.IsDefault, now); err != nil {
			if db.IsUniqueViolation(err) {
				return nameTaken()
			}
			return err
		}
		return s.record(ctx, tx, p, m, ActionCreate, "user_group", g.ID.String(), map[string]any{"after": g.auditView()})
	})
	if err != nil {
		return nil, err
	}
	s.Invalidate()
	return g, nil
}

// Update changes a group with optimistic locking (audit group.update).
// Setting isDefault: true moves the default flag here; the default group
// cannot be un-defaulted directly.
func (s *Service) Update(ctx context.Context, p *authz.Principal, id uuid.UUID, in Input, m Meta) (*Group, error) {
	now := s.now()
	var out *Group
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		before, err := scanGroup(tx.QueryRow(ctx, `SELECT `+groupCols+` FROM user_groups WHERE id = $1 FOR UPDATE`, id))
		if db.IsNoRows(err) {
			return apperr.NotFound("用户组")
		}
		if err != nil {
			return err
		}
		g := *before
		details := map[string]any{}
		in.apply(&g, false, details)
		if in.IsDefault != nil {
			if !*in.IsDefault && before.IsDefault {
				details["isDefault"] = "必须有一个默认组：请把另一个组设为默认"
			}
			if *in.IsDefault {
				g.IsDefault = true
			}
		}
		if len(details) > 0 {
			return apperr.Validation("用户组参数校验失败", details)
		}
		if before.Version != *in.Version {
			return apperr.VersionConflict()
		}
		if g.IsDefault && !before.IsDefault {
			if err := clearDefault(ctx, tx, id, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE user_groups SET name = $2, description = $3, price_multiplier_nano = $4, rpm = $5, rpd = $6,
			daily_spend_nano = $7, monthly_spend_nano = $8, timezone = $9, is_default = $10, version = version + 1, updated_at = $11
			WHERE id = $1`, id, g.Name, g.Description, int64(g.Multiplier), g.Limits.RPM, g.Limits.RPD, nanoPtr(g.Limits.DailySpend),
			nanoPtr(g.Limits.MonthlySpend), g.Timezone, g.IsDefault, now); err != nil {
			if db.IsUniqueViolation(err) {
				return nameTaken()
			}
			return err
		}
		g.Version, g.UpdatedAt = before.Version+1, now
		if g.Members, err = s.members(ctx, tx, id); err != nil {
			return err
		}
		out = &g
		return s.record(ctx, tx, p, m, ActionUpdate, "user_group", id.String(), map[string]any{"before": before.auditView(), "after": g.auditView()})
	})
	if err != nil {
		return nil, err
	}
	s.Invalidate()
	return out, nil
}

// Delete removes a non-default group; its members move to the default group
// (audit group.delete, notification account.group_changed for each member).
func (s *Service) Delete(ctx context.Context, p *authz.Principal, id uuid.UUID, m Meta) error {
	now := s.now()
	var moved []uuid.UUID
	var gone, def *Group
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var err error
		gone, err = scanGroup(tx.QueryRow(ctx, `SELECT `+groupCols+` FROM user_groups WHERE id = $1 FOR UPDATE`, id))
		if db.IsNoRows(err) {
			return apperr.NotFound("用户组")
		}
		if err != nil {
			return err
		}
		if gone.IsDefault {
			return apperr.New(apperr.KindConflict, CodeGroupDefault, "默认用户组不能删除，请先把另一个组设为默认")
		}
		if def, err = scanGroup(tx.QueryRow(ctx, `SELECT `+groupCols+` FROM user_groups WHERE is_default`)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM users WHERE group_id = $1 ORDER BY id`, id)
		if err != nil {
			return err
		}
		if moved, err = db.CollectRows[uuid.UUID](rows); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET group_id = $2, updated_at = $3 WHERE group_id = $1`, id, def.ID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_groups WHERE id = $1`, id); err != nil {
			return err
		}
		md := gone.auditView()
		md["movedMembers"], md["movedTo"] = len(moved), Ref{ID: def.ID, Name: def.Name}
		return s.record(ctx, tx, p, m, ActionDelete, "user_group", id.String(), md)
	})
	if err != nil {
		return err
	}
	s.Invalidate()
	s.membershipChanged()
	if s.OnUserMoved != nil {
		for _, uid := range moved {
			s.OnUserMoved(ctx, Change{UserID: uid, From: Ref{ID: gone.ID, Name: gone.Name}, To: def, Deleted: true, At: now})
		}
	}
	return nil
}

func (s *Service) membershipChanged() {
	if s.OnMembership != nil {
		s.OnMembership()
	}
}

// SetUserGroup moves userID into groupID (audit user.group_change,
// notification account.group_changed). Moving a user into the group they are
// already in changes nothing and returns (nil, nil).
func (s *Service) SetUserGroup(ctx context.Context, p *authz.Principal, userID, groupID uuid.UUID, m Meta) (*Change, error) {
	now := s.now()
	var change *Change
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		change = nil
		to, err := scanGroup(tx.QueryRow(ctx, `SELECT `+groupCols+` FROM user_groups WHERE id = $1`, groupID))
		if db.IsNoRows(err) {
			return apperr.Validation("参数校验失败", map[string]any{"groupId": "用户组不存在"})
		}
		if err != nil {
			return err
		}
		var from Ref
		var fromID *uuid.UUID
		var fromName *string
		err = tx.QueryRow(ctx, `SELECT u.group_id, g.name FROM users u LEFT JOIN user_groups g ON g.id = u.group_id
			WHERE u.id = $1 FOR UPDATE OF u`, userID).Scan(&fromID, &fromName)
		if db.IsNoRows(err) {
			return apperr.NotFound("用户")
		}
		if err != nil {
			return err
		}
		if fromID != nil {
			from.ID = *fromID
		}
		if fromName != nil {
			from.Name = *fromName
		}
		if fromID != nil && *fromID == groupID {
			return nil
		}
		// The user's version is left alone: the group has its own endpoint
		// and must not invalidate a pending role / status edit.
		if _, err := tx.Exec(ctx, `UPDATE users SET group_id = $2, updated_at = $3 WHERE id = $1`,
			userID, groupID, now); err != nil {
			return err
		}
		change = &Change{UserID: userID, From: from, To: to, At: now}
		return s.record(ctx, tx, p, m, ActionUserChange, "user", userID.String(), map[string]any{
			"before": from, "after": Ref{ID: to.ID, Name: to.Name}})
	})
	if err != nil || change == nil {
		return nil, err
	}
	s.membershipChanged()
	if s.OnUserMoved != nil {
		s.OnUserMoved(ctx, *change)
	}
	return change, nil
}
