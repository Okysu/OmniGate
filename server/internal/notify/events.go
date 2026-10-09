// Package notify implements notifications (docs/contracts/phase6-api.md):
// events with dedupe keys fanned out to in-app notifications and an email /
// webhook outbox, user preferences, the delivery worker (SMTP mailer, webhook
// formats, daily digest, hourly rate limit with an overflow summary), signed
// one-click unsubscribe links, and the scanners and hooks that produce events.
package notify

import (
	"slices"

	"omnigate/internal/authz"
	"omnigate/internal/identity"
)

// Event types (§2).
const (
	TypeWalletBalanceLow      = "wallet.balance_low"
	TypeWalletCredited        = "wallet.credited"
	TypeSubscriptionExpiring  = "subscription.expiring"
	TypeSubscriptionExpired   = "subscription.expired"
	TypeQuotaNearLimit        = "quota.near_limit"
	TypeQuotaExhausted        = "quota.exhausted"
	TypeModelPriceChanged     = "model.price_changed"
	TypeModelRemoved          = "model.removed"
	TypeModelAdded            = "model.added"
	TypeKeyExpiring           = "key.expiring"
	TypeChannelUnhealthy      = "channel.unhealthy"
	TypeChannelRecovered      = "channel.recovered"
	TypeChannelAuthFailed     = "channel.auth_failed"
	TypeUpstreamBalanceLow    = "upstream.balance_low"
	TypePluginPendingApproval = "plugin.pending_approval"
	// phase7-api.md §2, §3, §4.9.
	TypeAccountStatusChanged   = "account.status_changed"
	TypeSubscriptionQuotaReset = "subscription.quota_reset"
	TypeSubscriptionExtended   = "subscription.extended"
	// phase8-api.md §1.3, §2.2.
	TypeAccountGroupChanged = "account.group_changed"
	TypeLimitSpendNear      = "limit.spend_near"
	TypeLimitSpendReached   = "limit.spend_reached"
	// phase5-api.md §5.5.
	TypeChannelShareInvited = "channel.share_invited"
)

// Severities of notifications (§4).
const (
	SeverityInfo     = "info"
	SeverityWarn     = "warning"
	SeverityCritical = "critical"
)

// category groups event types by who may receive them.
type category int

const (
	catBilling  category = iota // wallet.*, subscription.*, quota.*, limit.*: billing.own
	catKeys                     // key.*: keys.own
	catModels                   // model.*: everyone
	catChannels                 // channel.*, upstream.*: channel owners and channels.manage
	catPlugins                  // plugin.*: plugins.trust
	catAccount                  // account.*: every user, delivered even while disabled
	catSharing                  // channel.share_invited: every user (recipients need not own channels)
)

// EventMeta describes one event type of the catalog.
type EventMeta struct {
	Type     string
	cat      category
	Email    bool // default email switch (§2 table)
	InApp    bool // default in-app switch; webhooks default to off (§6.1)
	Alert    bool // never merged into the daily digest (§3 digest)
	Severity string
	// InAppLocked: the in-app switch is always on (account.status_changed).
	InAppLocked bool
}

// Catalog lists every event type in display order.
var Catalog = []EventMeta{
	{TypeAccountStatusChanged, catAccount, true, true, true, SeverityWarn, true},
	{TypeAccountGroupChanged, catAccount, false, true, false, SeverityInfo, false},
	{TypeWalletBalanceLow, catBilling, true, true, true, SeverityWarn, false},
	{TypeWalletCredited, catBilling, false, true, false, SeverityInfo, false},
	{TypeSubscriptionExpiring, catBilling, true, true, false, SeverityWarn, false},
	{TypeSubscriptionExpired, catBilling, true, true, false, SeverityInfo, false},
	{TypeSubscriptionQuotaReset, catBilling, true, true, false, SeverityInfo, false},
	{TypeSubscriptionExtended, catBilling, true, true, false, SeverityInfo, false},
	{TypeQuotaNearLimit, catBilling, false, true, false, SeverityInfo, false},
	{TypeQuotaExhausted, catBilling, true, true, true, SeverityWarn, false},
	{TypeLimitSpendNear, catBilling, false, true, false, SeverityInfo, false},
	{TypeLimitSpendReached, catBilling, true, true, true, SeverityWarn, false},
	{TypeModelPriceChanged, catModels, true, true, false, SeverityInfo, false},
	{TypeModelRemoved, catModels, true, true, false, SeverityWarn, false},
	{TypeModelAdded, catModels, false, false, false, SeverityInfo, false},
	{TypeKeyExpiring, catKeys, true, true, false, SeverityWarn, false},
	{TypeChannelUnhealthy, catChannels, true, true, true, SeverityCritical, false},
	{TypeChannelRecovered, catChannels, false, true, true, SeverityInfo, false},
	{TypeChannelAuthFailed, catChannels, true, true, true, SeverityCritical, false},
	{TypeUpstreamBalanceLow, catChannels, true, true, true, SeverityWarn, false},
	{TypeChannelShareInvited, catSharing, false, true, false, SeverityInfo, false},
	{TypePluginPendingApproval, catPlugins, true, true, false, SeverityInfo, false},
}

// Meta returns the catalog entry of t.
func Meta(t string) (EventMeta, bool) {
	i := slices.IndexFunc(Catalog, func(m EventMeta) bool { return m.Type == t })
	if i < 0 {
		return EventMeta{}, false
	}
	return Catalog[i], true
}

// IsAlert reports whether t is an alert-class event (channel.*, upstream.*,
// quota.exhausted, wallet.balance_low).
func IsAlert(t string) bool {
	m, ok := Meta(t)
	return ok && m.Alert
}

// ReachesDisabled reports whether events of type t are delivered to disabled
// users (account.*: the user learns why the account was disabled).
func ReachesDisabled(t string) bool {
	m, ok := Meta(t)
	return ok && m.cat == catAccount
}

// Eligibility is what decides which events a user may receive.
type Eligibility struct {
	Role identity.Role
	// OwnsChannel: the user owns at least one channel.
	OwnsChannel bool
}

// Eligible reports whether a user may receive (and switch on) events of type t.
func (e Eligibility) Eligible(t string) bool {
	m, ok := Meta(t)
	if !ok {
		return false
	}
	p := &authz.Principal{Role: e.Role}
	switch m.cat {
	case catBilling:
		return p.Can(authz.BillingOwn)
	case catKeys:
		return p.Can(authz.KeysOwn)
	case catChannels:
		return e.OwnsChannel || p.Can(authz.ChannelsManage)
	case catPlugins:
		return p.Can(authz.PluginsTrust)
	case catSharing:
		return true
	}
	return true
}

// ChannelAlertTypes are the channel alert events (channel owners and
// channels.manage; the alerts summary lists their recent notifications).
func ChannelAlertTypes() []string {
	var out []string
	for _, m := range Catalog {
		if m.cat == catChannels {
			out = append(out, m.Type)
		}
	}
	return out
}

// rolesWith returns the roles granting perm.
func rolesWith(perm authz.Permission) []string {
	var out []string
	for _, r := range []identity.Role{identity.RoleSystemAdmin, identity.RoleChannelAdmin, identity.RoleUser, identity.RoleAuditor} {
		if slices.Contains(authz.Permissions(r), perm) {
			out = append(out, string(r))
		}
	}
	return out
}
