package app

import (
	"context"
	"log/slog"

	"omnigate/internal/audit"
	"omnigate/internal/config"
	"omnigate/internal/platform/db"
	"omnigate/internal/plugin"
	"omnigate/internal/plugin/engine"
	"omnigate/internal/seed"
)

// SeedServices builds the catalog services for `omnigate seed` the way New
// wires them for the admin API (same validation and audit), including custom
// plugin meters for plan rules, without starting the server or any worker.
func SeedServices(pool *db.DB, log *slog.Logger) seed.Services {
	svc := seed.NewServices(pool, log)
	plugins := plugin.NewService(pool, audit.NewRecorder(pool, log), log, engine.Config{})
	svc.Plans.CustomMeters = pluginMeters{plugins} // round9.go; only resolves meters on save
	return svc
}

// seedOnStart runs OMNIGATE_SEED_ON_START (seed.OnStart) with the server's
// plugin service resolving custom plan meters.
func seedOnStart(ctx context.Context, cfg *config.Config, log *slog.Logger, pool *db.DB, plugins *plugin.Service) seed.OnStartOutcome {
	svc := seed.NewServices(pool, log)
	svc.Plans.CustomMeters = pluginMeters{plugins}
	return seed.OnStart(ctx, svc, cfg.SeedOnStart, Version, log)
}
