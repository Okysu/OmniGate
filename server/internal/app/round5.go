package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/billing"
	"omnigate/internal/config"
	"omnigate/internal/identity"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/secretbox"
	"omnigate/internal/requestlog"
	"omnigate/internal/routing"
	"omnigate/internal/settings"
)

// round5 holds the route rules and system settings components
// (docs/contracts/phase4-api.md §2–§3).
type round5 struct {
	settings  *settings.Service
	settingsH *settings.Handler
	routes    *routing.Service
	routesH   *routing.Handler
}

// newRound5 creates the settings and routing services and hooks the settings
// into login admission, sign-up credit and billing enforcement.
func newRound5(ctx context.Context, cfg *config.Config, log *slog.Logger, pool *db.DB, rec *audit.Recorder,
	cur Currency, providers []auth.Provider, authSvc *auth.Service, bill *billing.Service, keys []secretbox.Key) (*round5, error) {
	login := []map[string]string{}
	for _, p := range providers {
		login = append(login, map[string]string{"id": p.ID(), "type": p.Type(), "displayName": p.DisplayName()})
	}
	domains := cfg.Auth.AllowedEmailDomains
	if domains == nil {
		domains = []string{}
	}
	box, err := secretbox.New("system-settings", keys)
	if err != nil {
		return nil, err
	}
	// Environment layer: settings that also have an OMNIGATE_* variable.
	env := map[string]any{
		"auth.registrationMode":    string(cfg.Auth.RegistrationMode),
		"auth.allowedEmailDomains": domains,
	}
	for k, v := range map[string]string{"host": cfg.SMTP.Host, "security": cfg.SMTP.Security, "username": cfg.SMTP.Username,
		"password": cfg.SMTP.Password, "from": cfg.SMTP.From} {
		if v != "" {
			env["notifications.smtp."+k] = v
		}
	}
	if cfg.SMTP.Port != 0 {
		env["notifications.smtp.port"] = int64(cfg.SMTP.Port)
	}
	st, err := settings.New(ctx, pool, rec, log, settings.Options{
		Env: env, Box: box, Production: cfg.IsProduction(),
		Readonly: map[string]any{
			"currency": cur, "publicUrl": cfg.PublicURL.String(), "channelsAllowPrivateNetwork": cfg.ChannelsAllowPrivateNetwork,
			"loginProviders": login, "env": string(cfg.Env),
		},
	})
	if err != nil {
		return nil, err
	}
	bill.UseSettings(st)
	authSvc.Registration = st.Registration
	authSvc.OnUserCreated = func(ctx context.Context, u *identity.User, m auth.RequestMeta) error {
		return bill.GrantSignupCredit(ctx, billing.Actor{ID: u.ID, Name: u.DisplayName}, st.SignupCredit(ctx),
			billing.RequestMeta{IPPrefix: m.IPPrefix, RequestID: m.RequestID})
	}
	routes := routing.NewService(pool, rec, log)
	if err := routes.Reload(ctx); err != nil {
		return nil, err
	}
	return &round5{settings: st, settingsH: settings.NewHandler(st), routes: routes}, nil
}

// workers are the background jobs of the round 5 components.
// extra jobs share the daily request-log retention schedule.
func (r *round5) workers(ctx context.Context, pool *db.DB, log *slog.Logger, extra ...func(context.Context, time.Time)) []func() {
	return []func(){
		func() { r.routes.Run(ctx) },
		func() { requestlog.RunRetention(ctx, pool, log, r.settings.LogRetentionDays, extra...) },
	}
}

// systemInfo adds the public site settings to /api/system/info.
func (r *round5) systemInfo(req *http.Request, info map[string]any) {
	s := r.settings.Current(req.Context())
	info["siteName"] = s.Site.Name
	info["announcement"] = s.Site.Announcement
	info["landingEnabled"] = s.Site.LandingEnabled
	info["docsUrl"] = s.Site.DocsURL
	info["publicModelPlaza"] = s.Site.PublicModelPlaza
	info["registrationMode"] = s.Auth.RegistrationMode
}
