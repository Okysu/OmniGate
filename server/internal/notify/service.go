package notify

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"omnigate/internal/audit"
	"omnigate/internal/channel"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/netguard"
	"omnigate/internal/platform/secretbox"
	"omnigate/internal/settings"
)

var metricSent = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "omnigate_notifications_sent_total",
	Help: "Notifications by channel (email, webhook, inapp), event type and result.",
}, []string{"channel", "type", "result"})

// Metric results.
const (
	resultSent    = "sent"
	resultFailed  = "failed"
	resultRetry   = "retry"
	resultSkipped = "skipped"
	resultLimited = "rate_limited"
)

// Settings is the part of the system settings notifications use.
type Settings interface {
	SMTP(ctx context.Context) settings.SMTPConfig
	NotificationsEnabled(ctx context.Context) bool
	EmailRateLimitPerHour(ctx context.Context) int
	SiteName(ctx context.Context) string
}

// Models is the channel registry view used by the model scanners.
type Models interface {
	Models(userID uuid.UUID, allowed []uuid.UUID) map[string]int
	Active() []*channel.Runtime
}

// Options configure the service.
type Options struct {
	Pool     *db.DB
	Log      *slog.Logger
	Audit    *audit.Recorder
	Settings Settings
	// Keys are the master keys (ADR-0008): webhook secrets and unsubscribe
	// tokens are sealed with keys derived from them.
	Keys      []secretbox.Key
	PublicURL *url.URL
	// Production refuses unencrypted SMTP.
	Production bool
	// AllowPrivateNetwork mirrors OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK:
	// webhooks of users holding channels.manage may then reach private
	// addresses (the same rule as channels).
	AllowPrivateNetwork bool
	Proxy               *url.URL
	// WebhookTransport replaces the SSRF-guarded transport (tests).
	WebhookTransport http.RoundTripper
	// SMTPTLS is the base TLS configuration for SMTP (tests: custom roots).
	SMTPTLS *tls.Config
	// Models enables the model added/removed scanners.
	Models Models
	// Currency is the settlement currency code shown in wallet messages.
	Currency string
	Now      func() time.Time
}

// Service emits, stores and delivers notifications.
type Service struct {
	pool  *db.DB
	log   *slog.Logger
	audit *audit.Recorder
	set   Settings
	opts  Options
	clock atomic.Pointer[func() time.Time]

	secretBox *secretbox.Keyring // webhook secrets
	tokenBox  *secretbox.Keyring // unsubscribe tokens

	guarded *http.Client
	open    *http.Client

	wake      chan struct{}
	modelScan chan struct{}
	async     sync.WaitGroup
	sem       chan struct{}

	// walletQ serializes wallet events per user: handlers run in submission
	// order, and events older than the newest handled ledger entry are not
	// allowed to flip the low-balance mark (see WalletChanged).
	walletQ walletQueue

	mu         sync.Mutex
	recent     map[string]time.Time // dedupe keys seen recently (skips a DB round trip)
	thresholds map[uuid.UUID]cachedThreshold
	authSeen   map[uuid.UUID]time.Time // channel.auth_failed throttle (per instance)
	// lastModelSig is the registry fingerprint of the last model scan.
	lastModelSig string
	scanMu       sync.Mutex // serializes ScanModels on this instance
}

// New creates the service.
func New(o Options) (*Service, error) {
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	sb, err := secretbox.New("notification-webhook-secret", o.Keys)
	if err != nil {
		return nil, err
	}
	tb, err := secretbox.New("notification-token", o.Keys)
	if err != nil {
		return nil, err
	}
	s := &Service{pool: o.Pool, log: o.Log, audit: o.Audit, set: o.Settings, opts: o,
		secretBox: sb, tokenBox: tb,
		wake: make(chan struct{}, 1), modelScan: make(chan struct{}, 1), sem: make(chan struct{}, 32),
		recent: map[string]time.Time{}, thresholds: map[uuid.UUID]cachedThreshold{}, authSeen: map[uuid.UUID]time.Time{},
	}
	s.SetClock(o.Now)
	s.guarded = &http.Client{Transport: netguard.NewTransport(netguard.Options{Proxy: o.Proxy}), CheckRedirect: netguard.NoRedirect,
		Timeout: webhookTimeout}
	s.open = &http.Client{Transport: netguard.NewTransport(netguard.Options{AllowPrivate: true, Proxy: o.Proxy}),
		CheckRedirect: netguard.NoRedirect, Timeout: webhookTimeout}
	if o.WebhookTransport != nil {
		s.guarded = &http.Client{Transport: o.WebhookTransport, CheckRedirect: netguard.NoRedirect, Timeout: webhookTimeout}
		s.open = s.guarded
	}
	return s, nil
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.clock.Store(&now) }

func (s *Service) now() time.Time { return (*s.clock.Load())() }

// goAsync runs fn in the background (hooks called from request paths must
// not block them). At most 32 run at once; beyond that fn runs inline.
func (s *Service) goAsync(fn func(ctx context.Context)) {
	ctx := context.Background()
	s.async.Add(1)
	select {
	case s.sem <- struct{}{}:
		go func() {
			defer func() { <-s.sem; s.async.Done() }()
			fn(ctx)
		}()
	default:
		defer s.async.Done()
		fn(ctx)
	}
}

// Wait waits for background emissions (tests, shutdown).
func (s *Service) Wait() { s.async.Wait() }

// kick wakes the delivery worker.
func (s *Service) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// link turns an in-site path into an absolute URL.
func (s *Service) link(path string) string {
	if path == "" {
		return ""
	}
	if s.opts.PublicURL == nil {
		return path
	}
	return s.opts.PublicURL.Scheme + "://" + s.opts.PublicURL.Host + path
}

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }
