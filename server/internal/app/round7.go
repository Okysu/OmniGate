package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/adminapi"
	"omnigate/internal/audit"
	"omnigate/internal/channel"
	gwkeys "omnigate/internal/keys"
	"omnigate/internal/limits"
	"omnigate/internal/notify"
	"omnigate/internal/platform/db"
	"omnigate/internal/usergroup"
)

// round7 holds the user groups and limits (docs/contracts/phase8-api.md §1–§3).
type round7 struct {
	pool    *db.DB
	log     *slog.Logger
	groups  *usergroup.Service
	groupsH *usergroup.Handler
	limits  *limits.Service
	limitsH *limits.Handler
}

// newRound7 creates the group and limits services: membership changes drop
// cached key authentications and reload the channel registry (group shares),
// moved users and spend limits at ≥ 80 % are notified.
func newRound7(log *slog.Logger, pool *db.DB, rec *audit.Recorder, keySvc *gwkeys.Service, reg *channel.Registry,
	admin *adminapi.Handler, n *notify.Service) *round7 {
	groups := usergroup.NewService(pool, rec, log)
	groups.OnMembership = func() {
		keySvc.InvalidateCache()
		reg.Invalidate()
	}
	groups.OnUserMoved = n.AccountGroupChanged
	lim := limits.NewService(pool, groups, log)
	lim.OnSpend = n.SpendLimit
	admin.Groups = groups
	return &round7{pool: pool, log: log, groups: groups, groupsH: usergroup.NewHandler(groups, admin.Users()), limits: lim,
		limitsH: limits.NewHandler(lim, keySvc)}
}

// meGroup is the group part of /api/me.
func (r *round7) meGroup(ctx context.Context, userID uuid.UUID) (any, error) {
	g, err := r.groups.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return g.Self(), nil
}

// prune deletes old usage counters (daily, with request-log retention).
func (r *round7) prune(ctx context.Context, now time.Time) {
	if n, err := limits.Prune(ctx, r.pool, now); err != nil && ctx.Err() == nil {
		r.log.Error("usage counter pruning failed", "err", err)
	} else if n > 0 {
		r.log.Info("usage counters pruned", "deleted", n)
	}
}
