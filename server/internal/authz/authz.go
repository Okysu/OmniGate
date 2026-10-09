// Package authz implements role-based permissions plus resource scope checks.
//
// Round 1 ships a code-defined role → permission table (ADR-0004). Every check
// is performed server-side; the permission list returned by /api/me only drives
// UI visibility. Resource-level checks (owner / shared / global scope) build on
// top of Can via CanOnResource as channels and keys land in later rounds.
package authz

import (
	"slices"

	"github.com/google/uuid"

	"omnigate/internal/identity"
)

type Permission string

const (
	UsersRead      Permission = "users.read"
	UsersWrite     Permission = "users.write"
	AuditRead      Permission = "audit.read"
	SettingsRead   Permission = "settings.read"
	SettingsWrite  Permission = "settings.write"
	ChannelsRead   Permission = "channels.read"   // channels visible to the actor
	ChannelsWrite  Permission = "channels.write"  // own channels
	ChannelsManage Permission = "channels.manage" // global/shared channels of others
	ModelsManage   Permission = "models.manage"
	RoutesManage   Permission = "routes.manage"
	KeysOwn        Permission = "keys.own"
	PluginsRead    Permission = "plugins.read"
	PluginsManage  Permission = "plugins.manage"
	PluginsTrust   Permission = "plugins.trust"
	BillingOwn     Permission = "billing.own"    // own wallet, subscriptions, redeem codes
	BillingManage  Permission = "billing.manage" // plans, prices, issuing redeem codes
	StatsOwn       Permission = "stats.own"
	StatsAll       Permission = "stats.all"
)

var userBase = []Permission{ChannelsRead, ChannelsWrite, KeysOwn, PluginsRead, BillingOwn, StatsOwn}

var rolePermissions = map[identity.Role][]Permission{
	identity.RoleSystemAdmin: {
		UsersRead, UsersWrite, AuditRead, SettingsRead, SettingsWrite,
		ChannelsRead, ChannelsWrite, ChannelsManage, ModelsManage, RoutesManage,
		KeysOwn, PluginsRead, PluginsManage, PluginsTrust, BillingOwn, BillingManage,
		StatsOwn, StatsAll,
	},
	identity.RoleChannelAdmin: append(slices.Clone(userBase),
		ChannelsManage, ModelsManage, RoutesManage, PluginsManage, StatsAll),
	identity.RoleUser: userBase,
	// Auditors can read configuration, statistics and audit logs but never secrets
	// and cannot write anything (not even own keys).
	identity.RoleAuditor: {UsersRead, AuditRead, SettingsRead, ChannelsRead, PluginsRead, StatsOwn, StatsAll},
}

// Principal is the authenticated actor of a request.
type Principal struct {
	UserID    uuid.UUID
	Name      string
	Role      identity.Role
	SessionID uuid.UUID
}

// Can reports whether the principal's role grants perm.
func (p *Principal) Can(perm Permission) bool {
	if p == nil {
		return false
	}
	return slices.Contains(rolePermissions[p.Role], perm)
}

// Permissions returns the effective permission list for a role.
func Permissions(r identity.Role) []Permission {
	return slices.Clone(rolePermissions[r])
}

// Scope of a shareable resource (channel, gateway key, plan).
type Scope string

const (
	ScopePrivate Scope = "private"
	ScopeShared  Scope = "shared"
	ScopeGlobal  Scope = "global"
)

// Action on a scoped resource.
type Action string

const (
	ActionUse    Action = "use"    // call through the gateway
	ActionView   Action = "view"   // read non-secret configuration
	ActionManage Action = "manage" // edit, rotate secrets, delete
)

// Resource describes a scoped resource for CanOnResource.
type Resource struct {
	OwnerID  uuid.UUID
	Scope    Scope
	SharedTo func(uuid.UUID, identity.Role) bool // only consulted for ScopeShared
}

// CanOnResource evaluates role + action + resource + scope. Sharing grants "use"
// only: shared/global users can never view secrets or edit the resource.
func (p *Principal) CanOnResource(a Action, r Resource, managePerm Permission) bool {
	if p == nil {
		return false
	}
	if r.OwnerID == p.UserID {
		return true
	}
	if p.Can(managePerm) {
		return true
	}
	if a != ActionUse {
		return false
	}
	switch r.Scope {
	case ScopeGlobal:
		return true
	case ScopeShared:
		return r.SharedTo != nil && r.SharedTo(p.UserID, p.Role)
	default:
		return false
	}
}
