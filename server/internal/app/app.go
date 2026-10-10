// Package app wires configuration, infrastructure and HTTP routing together.
//
// Two route trees share one process but never share middleware: the control
// plane (/api, admin UI) and the data plane (/v1). A failure in a control-plane
// handler is contained by its own recovery middleware and cannot affect /v1.
package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"omnigate/internal/adminapi"
	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/billing"
	"omnigate/internal/channel"
	"omnigate/internal/config"
	"omnigate/internal/gateway"
	"omnigate/internal/identity"
	"omnigate/internal/journal"
	gwkeys "omnigate/internal/keys"
	"omnigate/internal/notify"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/platform/secretbox"
	"omnigate/internal/plaza"
	"omnigate/internal/plugin"
	"omnigate/internal/plugin/engine"
	"omnigate/internal/pricing"
	"omnigate/internal/requestlog"
	"omnigate/internal/routing"
	"omnigate/internal/subscription"
	"omnigate/internal/webui"
)

// Build information, set at build time via -ldflags
// "-X omnigate/internal/app.Version=... -X omnigate/internal/app.Commit=... -X omnigate/internal/app.BuildTime=...".
var (
	Version   = "dev"
	Commit    = "" // git commit SHA
	BuildTime = "" // RFC 3339
)

type App struct {
	draining  atomic.Bool // set on shutdown: readiness reports 503
	opts      Options
	cfg       *config.Config
	log       *slog.Logger
	pool      *db.DB
	authH     *auth.Handler
	adminH    *adminapi.Handler
	providers []auth.Provider
	currency  Currency

	registry *channel.Registry
	channelH *channel.Handler
	keysH    *gwkeys.Handler
	pricingH *pricing.Handler
	logsH    *requestlog.Handler
	billing  *billing.Service
	billingH *billing.Handler
	logs     *requestlog.Writer
	gw       *gateway.Gateway
	plugins  *plugin.Service
	pluginH  *plugin.Handler
	subsH    *subscription.Handler
	subs     *subscription.Service
	channels *channel.Service
	web      http.Handler // nil when OMNIGATE_WEB_ENABLED=false
	workers  sync.WaitGroup
	r5       *round5 // route rules and system settings
	r6       *round6 // notifications
	r7       *round7 // user groups and limits
	plazaH   *plaza.Handler
	journal  *journal.Journal // settlement journal (round10.go, ADR-0010)
}

// Options lets tests inject dependencies.
type Options struct {
	HTTPClient *http.Client // used for IdP calls
	// UpstreamTransport replaces the upstream network policy for all channels
	// (tests: regular users' channels may then reach loopback fake upstreams).
	UpstreamTransport http.RoundTripper
	// WebhookTransport replaces the SSRF-guarded transport of notification
	// webhooks (tests: lets webhooks reach loopback fakes).
	WebhookTransport http.RoundTripper
	// SMTPTLS is the base TLS configuration of the SMTP client (tests).
	SMTPTLS *tls.Config
	// ShutdownGrace overrides how long in-flight requests may run after a
	// shutdown starts (default 30s; tests use a short one).
	ShutdownGrace time.Duration
	// WrapBilling wraps the gateway's billing (tests: settlement failures).
	WrapBilling func(gateway.Billing) gateway.Billing
	// BeforeLogInsert runs before every request log insert; an error fails
	// the insert (tests: request log failures).
	BeforeLogInsert func(context.Context) error
}

func New(ctx context.Context, cfg *config.Config, log *slog.Logger, pool *db.DB, opts Options) (*App, error) {
	if err := validate(cfg, log); err != nil {
		return nil, err
	}
	keys := make([]secretbox.Key, len(cfg.MasterKeys))
	for i, k := range cfg.MasterKeys {
		keys[i] = secretbox.Key{ID: k.ID, Raw: k.Key}
	}
	stateBox, err := secretbox.New("oauth-state", keys)
	if err != nil {
		return nil, err
	}
	cur, err := ensureCurrency(ctx, pool, cfg.Currency, log)
	if err != nil {
		return nil, err
	}

	users := identity.NewStore(pool)
	rec := audit.NewRecorder(pool, log)
	svc := auth.NewService(cfg.Auth, !cfg.IsProduction(), auth.IsLoopbackHost(cfg.PublicURL.Hostname()), users, rec, log)
	providers := auth.BuildProviders(cfg.Auth, opts.HTTPClient)

	channelBox, err := secretbox.New("channel-secret", keys)
	if err != nil {
		return nil, err
	}
	if err := requestlog.EnsurePartitions(ctx, pool, time.Now().UTC()); err != nil {
		return nil, err
	}
	ua := "OmniGate/" + Version
	plugins := plugin.NewService(pool, rec, log, engine.Config{HeapLimit: uint64(cfg.PluginHeapLimitMB) << 20})
	if err := plugins.Seed(ctx); err != nil {
		return nil, fmt.Errorf("install built-in plugins: %w", err)
	}
	// OMNIGATE_SEED_ON_START: after the currency is stored and the built-in
	// plugins (custom plan meters) exist; once per database, never fatal.
	seedOnStart(ctx, cfg, log, pool, plugins)
	chStore := channel.NewStore(pool, channelBox)
	reg := channel.NewRegistry(chStore, log, channel.RegistryOptions{
		AllowPrivateNetwork: cfg.ChannelsAllowPrivateNetwork, Proxy: cfg.UpstreamProxy, Plugins: plugins,
		Transport: opts.UpstreamTransport,
	})
	plugins.OnChange = func() {
		_ = plugins.RefreshStatus(context.Background())
		reg.Invalidate()
	}
	if err := reg.Reload(ctx); err != nil {
		return nil, fmt.Errorf("load channels: %w", err)
	}
	keySvc := gwkeys.NewService(pool, rec)
	prices := pricing.NewService(pool, rec)
	bill := billing.NewService(pool, rec, log)
	subs := subscription.NewService(pool, rec, log)
	subs.CustomMeters = pluginMeters{plugins} // round9.go
	logs := requestlog.NewWriter(pool, log)
	admin := adminapi.New(users, rec)
	admin.OnUserChanged = func() {
		keySvc.InvalidateCache()
		// Channel tiers follow the owner's role (phase5-api.md §1).
		reg.Invalidate()
	}
	chSvc := channel.NewService(chStore, reg, rec, plugins, ua)
	r5, err := newRound5(ctx, cfg, log, pool, rec, cur, providers, svc, bill, keys)
	if err != nil {
		return nil, err
	}
	r6, err := newRound6(cfg, log, pool, rec, keys, cur, r5.settings, reg, chSvc, bill, subs, prices, plugins, opts)
	if err != nil {
		return nil, err
	}
	r7 := newRound7(log, pool, rec, keySvc, reg, admin, r6.notify)
	// User management (phase7-api.md §2): detail sources and notifications.
	admin.Pool, admin.Keys, admin.Subscriptions, admin.SessionIdle, admin.Log = pool, keySvc, subs, cfg.Auth.SessionIdleTTL, log
	admin.OnStatusChanged = r6.notify.AccountStatusChanged
	jrn, err := openJournal(cfg, log, pool, logs, opts) // round10.go
	if err != nil {
		return nil, err
	}
	gw := gateway.New(keySvc, reg, prices, gatewayBilling(bill, opts), subs, logs, log, gateway.Options{UserAgent: ua, Limits: r5.settings, Routes: r5.routes,
		Usage: r7.limits, Journal: jrn, Affinity: r5.affinity})
	handleJournal(jrn, gw, logs)
	r5.routesH = routing.NewHandler(r5.routes, users, gw)
	var web http.Handler
	if cfg.WebEnabled {
		ui, err := webui.New(webui.Options{Dir: cfg.WebDir, NotFound: http.HandlerFunc(notFound)})
		if err != nil {
			return nil, fmt.Errorf("OMNIGATE_WEB_DIR: %w", err)
		}
		if ui.Source() == webui.SourcePlaceholder {
			log.Warn("web app not embedded in this build (run `make build`); serving a placeholder page")
		}
		web = ui
	}

	authH := auth.NewHandler(svc, providers, stateBox, cfg.PublicURL, cfg.Auth.CookieSecure, log)
	authH.MeGroup = r7.meGroup
	plazaH := plaza.NewHandler(plaza.NewInfoService(pool, rec), reg, prices, subs, r5.settings, cur)
	plazaH.Groups = r7.groups
	return &App{
		opts: opts,
		cfg:  cfg, log: log, pool: pool, providers: providers, currency: cur,
		authH:    authH,
		adminH:   admin,
		registry: reg,
		channelH: channel.NewHandler(chSvc),
		channels: chSvc,
		plugins:  plugins,
		pluginH:  plugin.NewHandler(plugins),
		keysH:    gwkeys.NewHandler(keySvc),
		pricingH: pricing.NewHandler(prices, reg),
		logsH:    requestlog.NewHandler(pool),
		billing:  bill,
		billingH: billingHandler(bill, cfg.PublicURL.String()),
		logs:     logs,
		gw:       gw,
		subsH:    subscription.NewHandler(subs),
		subs:     subs,
		web:      web,
		r5:       r5,
		r6:       r6,
		r7:       r7,
		plazaH:   plazaH,
		journal:  jrn,
	}, nil
}

// Start launches background workers; they stop when ctx is cancelled and
// Stop waits for them (request logs are drained on the way out).
func (a *App) Start(ctx context.Context) {
	for _, f := range append([]func(){
		func() { a.registry.Run(ctx) },
		func() { a.logs.Run(ctx) },
		func() { requestlog.RunPartitionMaintenance(ctx, a.pool, a.log) },
		func() { a.billing.RunSweeper(ctx, time.Minute) },
		func() { a.plugins.Engine().RunWatchdog(ctx) },
		func() { a.channels.RunCapabilityScheduler(ctx, a.log) },
		func() { a.channels.RunRecoveryProber(ctx, a.log) },
		func() { a.subs.RunSweeper(ctx, time.Hour) },
		func() { a.adminH.RunAutoEnable(ctx, time.Minute) },
		func() { a.r6.notify.Run(ctx) },
		func() { a.r6.notify.RunScanners(ctx) },
		func() { a.journal.Run(ctx, journalInterval) },
	}, a.r5.workers(ctx, a.pool, a.log, a.r6.notify.Retention, a.r7.prune, a.billing.PruneSettled)...) {
		a.workers.Add(1)
		go func() { defer a.workers.Done(); f() }()
	}
}

// Stop waits for in-flight settlement and background workers to finish.
func (a *App) Stop() {
	a.gw.Wait()
	a.workers.Wait()
	a.r6.notify.Wait()
	a.closeJournal() // after everything that may still journal (round10.go)
}

// DB exposes the database (tests).
func (a *App) DB() *db.DB { return a.pool }

// EnableDueUsers runs the auto-enable job once (tests).
func (a *App) EnableDueUsers(ctx context.Context) (int, error) { return a.adminH.EnableDue(ctx) }

// Notifications exposes the notification service (tests).
func (a *App) Notifications() *notify.Service { return a.r6.notify }

// FlushLogs synchronously writes queued request logs (tests).
func (a *App) FlushLogs(ctx context.Context) error {
	a.gw.Wait()
	return a.logs.Flush(ctx)
}

// validate enforces secure defaults; production refuses unsafe configurations.
func validate(cfg *config.Config, log *slog.Logger) error {
	if len(cfg.MasterKeys) == 0 {
		if cfg.IsProduction() {
			return errors.New("OMNIGATE_MASTER_KEY is required in production (generate one with `omnigate keygen`)")
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		cfg.MasterKeys = []config.MasterKey{{ID: "ephemeral", Key: raw}}
		log.Warn("OMNIGATE_MASTER_KEY not set: using an ephemeral key; encrypted data will be unreadable after restart")
	}
	if cfg.IsProduction() && len(cfg.Auth.BootstrapAdmins) == 0 {
		return errors.New("OMNIGATE_BOOTSTRAP_ADMINS is required in production (e.g. github-id:12345)")
	}
	if cfg.Auth.GitHub == nil && len(cfg.Auth.OIDC) == 0 {
		log.Warn("no login provider configured: set OMNIGATE_AUTH_GITHUB_CLIENT_ID or OMNIGATE_AUTH_OIDC")
	}
	return nil
}

// Handler returns the root HTTP handler.
func (a *App) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.Base(a.log, a.cfg.TrustedProxies))
	r.Use(gateway.CollapseDuplicateV1)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", a.ready)

	// Data plane.
	r.Route("/v1", func(r chi.Router) {
		r.Use(httpx.AccessLog(a.log, "data"))
		a.gw.Routes(r)
	})

	// Control plane.
	origins := append([]string{a.cfg.PublicURL.Scheme + "://" + a.cfg.PublicURL.Host}, a.cfg.AllowedOrigins...)
	r.Route("/api", func(r chi.Router) {
		r.Use(httpx.AccessLog(a.log, "control"), auth.CSRF(origins))
		r.Get("/system/info", a.systemInfo)
		r.With(a.authH.OptionalSession).Group(a.plazaH.PublicRoutes)
		a.r6.h.PublicRoutes(r)
		a.authH.Routes(r)
		r.Group(func(r chi.Router) {
			r.Use(a.authH.RequireSession)
			a.authH.SessionRoutes(r)
			a.channelH.Routes(r)
			a.keysH.Routes(r)
			a.pricingH.Routes(r)
			a.logsH.Routes(r)
			a.billingH.Routes(r)
			a.pluginH.Routes(r)
			a.subsH.Routes(r)
			a.plazaH.Routes(r)
			a.r6.h.Routes(r)
			a.r7.limitsH.Routes(r)
			r.Route("/admin", func(r chi.Router) {
				a.r6.h.AdminRoutes(r)
				a.adminH.Routes(r)
				a.r7.groupsH.AdminRoutes(r)
				a.pricingH.AdminRoutes(r)
				a.billingH.AdminRoutes(r)
				a.subsH.AdminRoutes(r)
				a.r5.settingsH.AdminRoutes(r)
				a.r5.routesH.AdminRoutes(r)
				a.r5.affinityH.AdminRoutes(r)
				a.plazaH.AdminRoutes(r)
			})
		})
		r.NotFound(notFound)
	})

	// Web app: static files plus history-API fallback for every other GET/HEAD
	// (backend paths that get here still answer with the JSON 404).
	if a.web != nil {
		r.Handle("/*", a.web)
	} else {
		r.NotFound(notFound)
	}
	return r
}

func notFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{
		"code": "not_found", "message": "接口不存在", "requestId": httpx.RequestID(r.Context())}})
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	if a.draining.Load() {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "shutting_down"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.pool.Ping(ctx); err != nil {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "database_unavailable", "journal": a.journalStatus()})
		return
	}
	st, err := db.MigrationStatus(ctx, a.pool)
	if err == nil && st.TooNew {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "schema_too_new", "migrations": st})
		return
	}
	if err != nil || st.Pending > 0 {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "migrations_pending", "migrations": st})
		return
	}
	if js := a.journalStatus(); js.Pending > 0 {
		// Settlement work is waiting in the local journal: still serving
		// (the replayer catches up), but not fully healthy.
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "degraded", "migrations": st, "journal": js})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ready", "migrations": st, "journal": a.journalStatus()})
}

func (a *App) systemInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{
		"name":             "OmniGate",
		"version":          Version,
		"currency":         a.currency,
		"registrationMode": a.cfg.Auth.RegistrationMode,
	}
	a.r5.systemInfo(r, info)
	httpx.WriteJSON(w, http.StatusOK, info)
}

// Run serves HTTP and metrics until ctx is cancelled, then shuts down gracefully.
// In-flight streaming responses get up to 30s to finish.
func (a *App) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.cfg.HTTPAddr)
	if err != nil {
		return err
	}
	return a.Serve(ctx, ln)
}

// Serve is Run on an existing listener (tests).
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: streaming responses can legitimately run for minutes.
	}
	metrics := &http.Server{Addr: a.cfg.MetricsAddr, Handler: promhttp.Handler(), ReadHeaderTimeout: 5 * time.Second}

	// Workers outlive the HTTP server so in-flight requests are still logged and settled.
	workerCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	a.Start(workerCtx)

	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(ln) }()
	if a.cfg.MetricsAddr != "" && a.cfg.MetricsAddr != "off" {
		go func() { errc <- metrics.ListenAndServe() }()
	}
	a.log.Info("omnigate listening", "addr", a.cfg.HTTPAddr, "metrics", a.cfg.MetricsAddr,
		"public_url", a.cfg.PublicURL.String(), "env", a.cfg.Env, "version", Version, "commit", Commit, "build_time", BuildTime)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			// A listener failed (e.g. port in use): drain what started, then report it.
			_ = a.shutdown(srv, metrics, stopWorkers)
			return err
		}
	}
	return a.shutdown(srv, metrics, stopWorkers)
}

// defaultShutdownGrace is how long in-flight requests (including long
// streams) may finish after SIGTERM before their connections are closed.
// Container runtimes must allow more than this (docker --stop-timeout 40).
const defaultShutdownGrace = 30 * time.Second

// shutdown drains in order so nothing that was served is lost:
//  1. readiness reports 503 so load balancers stop sending traffic;
//  2. the HTTP server stops accepting and waits for in-flight requests up to
//     the grace period (30s), then closes the remaining connections (their handlers
//     still finish: settlement and logging happen after the client is gone);
//  3. wait for every gateway request and its settlement;
//  4. only then stop the workers (request log writer, notifications, …),
//     which flush what they still hold.
//
// A graceful stop returns nil (exit code 0) even when streams had to be cut.
func (a *App) shutdown(srv, metrics *http.Server, stopWorkers context.CancelFunc) error {
	a.draining.Store(true)
	grace := a.opts.ShutdownGrace
	if grace <= 0 {
		grace = defaultShutdownGrace
	}
	a.log.Info("shutting down: draining in-flight requests", "grace", grace)
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		a.log.Warn("grace period over: closing remaining connections", "err", err)
		_ = srv.Close()
	}
	a.gw.Wait()
	stopWorkers()
	a.Stop()
	mctx, mcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer mcancel()
	_ = metrics.Shutdown(mctx)
	a.log.Info("shutdown complete")
	return nil
}

// Currency is the deployment-wide settlement currency (ADR-0006).
type Currency struct {
	Code     string `json:"code"`
	Symbol   string `json:"symbol"`
	Decimals int    `json:"decimals"`
}

var knownCurrencies = map[string]Currency{
	"USD": {"USD", "$", 2}, "CNY": {"CNY", "¥", 2}, "EUR": {"EUR", "€", 2}, "GBP": {"GBP", "£", 2},
	"JPY": {"JPY", "¥", 0}, "HKD": {"HKD", "HK$", 2}, "SGD": {"SGD", "S$", 2},
}

// ensureCurrency records the settlement currency on first boot. Afterwards the
// stored value wins: changing currency on a live ledger needs an explicit
// migration (ADR-0006), never a silent env change.
func ensureCurrency(ctx context.Context, pool *db.DB, code string, log *slog.Logger) (Currency, error) {
	want, ok := knownCurrencies[code]
	if !ok {
		want = Currency{Code: code, Symbol: code, Decimals: 2}
	}
	raw, _ := json.Marshal(want)
	if _, err := pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ('billing.currency', $1)
		ON CONFLICT (key) DO NOTHING`, raw); err != nil {
		return Currency{}, fmt.Errorf("init currency: %w", err)
	}
	var stored Currency
	if err := pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = 'billing.currency'`).Scan(&stored); err != nil {
		return Currency{}, err
	}
	if stored.Code != want.Code {
		log.Warn("OMNIGATE_CURRENCY differs from the stored settlement currency; keeping stored value",
			"stored", stored.Code, "env", want.Code)
	}
	return stored, nil
}

// billingHandler serves /api/billing/*; publicURL builds invite links.
func billingHandler(bill *billing.Service, publicURL string) *billing.Handler {
	h := billing.NewHandler(bill)
	h.PublicURL = publicURL
	return h
}
