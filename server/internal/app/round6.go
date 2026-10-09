package app

import (
	"log/slog"

	"omnigate/internal/audit"
	"omnigate/internal/billing"
	"omnigate/internal/channel"
	"omnigate/internal/config"
	"omnigate/internal/notify"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/secretbox"
	"omnigate/internal/plugin"
	"omnigate/internal/pricing"
	"omnigate/internal/settings"
	"omnigate/internal/subscription"
)

// round6 holds the notification components (docs/contracts/phase6-api.md).
type round6 struct {
	notify *notify.Service
	h      *notify.Handler
}

// newRound6 creates the notification service and connects the event
// producers: wallet ledger, plan quotas, sell prices, plugin approvals,
// channel health (breaker, 401/403), upstream balances, registry reloads and
// channel share invitations.
func newRound6(cfg *config.Config, log *slog.Logger, pool *db.DB, rec *audit.Recorder, keys []secretbox.Key, cur Currency,
	st *settings.Service, reg *channel.Registry, chSvc *channel.Service, bill *billing.Service, subs *subscription.Service,
	prices *pricing.Service, plugins *plugin.Service, opts Options) (*round6, error) {
	n, err := notify.New(notify.Options{
		Pool: pool, Log: log, Audit: rec, Settings: st, Keys: keys, PublicURL: cfg.PublicURL, Production: cfg.IsProduction(),
		AllowPrivateNetwork: cfg.ChannelsAllowPrivateNetwork, Proxy: cfg.UpstreamProxy, WebhookTransport: opts.WebhookTransport,
		SMTPTLS: opts.SMTPTLS, Models: reg, Currency: cur.Code,
	})
	if err != nil {
		return nil, err
	}
	bill.OnLedger = n.WalletChanged
	subs.OnQuota = n.QuotaReached
	subs.OnBulk = n.SubscriptionBulk
	prices.OnSellPrice = n.SellPriceChanged
	plugins.OnPendingApproval = n.PluginPending
	reg.Breaker.OnTransition = n.ChannelTransition
	reg.OnAuthFailure = n.ChannelAuthFailed
	reg.OnReload = n.RegistryReloaded
	chSvc.OnBalance = n.BalanceRead
	chSvc.OnShareInvited = n.ShareInvited
	return &round6{notify: n, h: notify.NewHandler(n, chSvc)}, nil
}
