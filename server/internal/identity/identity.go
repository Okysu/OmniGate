// Package identity holds users, linked external identities and browser sessions.
package identity

import (
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleSystemAdmin  Role = "system_admin"
	RoleChannelAdmin Role = "channel_admin"
	RoleUser         Role = "user"
	RoleAuditor      Role = "auditor"
)

func (r Role) Valid() bool {
	switch r {
	case RoleSystemAdmin, RoleChannelAdmin, RoleUser, RoleAuditor:
		return true
	}
	return false
}

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

func (s Status) Valid() bool { return s == StatusActive || s == StatusDisabled }

type User struct {
	ID          uuid.UUID  `json:"id"`
	DisplayName string     `json:"displayName"`
	Email       *string    `json:"email"`
	AvatarURL   *string    `json:"avatarUrl"`
	Role        Role       `json:"role"`
	Status      Status     `json:"status"`
	Version     int        `json:"version"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
	Identities  []Identity `json:"identities"`
	// DisabledReason and DisabledUntil describe a suspension (phase7-api.md
	// §2.1); both are null for active users. DisabledUntil null = permanent.
	DisabledReason *string    `json:"disabledReason"`
	DisabledUntil  *time.Time `json:"disabledUntil"`
	// Group is the user's group (phase8-api.md §1.3; null only for rows
	// written by other tools before the default-group trigger ran).
	Group *GroupRef `json:"group"`

	groupID   *uuid.UUID
	groupName *string
}

// GroupRef names the user's group.
type GroupRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// ActiveAt reports whether the user may sign in and use API keys at now: the
// status is active, or the suspension has already ended (the auto-enable job
// flips the status shortly after).
func (u *User) ActiveAt(now time.Time) bool {
	return u.Status == StatusActive || (u.DisabledUntil != nil && !u.DisabledUntil.After(now))
}

// Status change actions reported to the user (notification account.status_changed).
const (
	ChangeDisabled = "disabled"
	ChangeEnabled  = "enabled"
	ChangeLogout   = "logout"
)

// StatusChange is a committed change of a user's account status, or a
// forced sign-out, made by an administrator or the auto-enable job.
type StatusChange struct {
	UserID uuid.UUID
	Action string // ChangeDisabled | ChangeEnabled | ChangeLogout
	Reason *string
	Until  *time.Time
	// Auto is set when the suspension ended automatically.
	Auto bool
	At   time.Time
}

// Identity is an external account (GitHub, OIDC) linked to a user.
type Identity struct {
	Provider string  `json:"provider"`
	Subject  string  `json:"-"`
	Login    *string `json:"login"`
	Email    *string `json:"email"`
}

// External is what an auth provider returns after a successful login.
type External struct {
	Provider      string
	Subject       string // immutable provider-side id
	Login         string // mutable handle (GitHub login, preferred_username)
	Name          string
	Email         string
	EmailVerified bool
	AvatarURL     string
}

type Session struct {
	ID         uuid.UUID `json:"id"`
	UserID     uuid.UUID `json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	UserAgent  string    `json:"userAgent"`
	IPPrefix   string    `json:"ipPrefix"`
	Current    bool      `json:"current"`
}

// UserPatch is an admin update guarded by optimistic locking.
type UserPatch struct {
	Role    *Role
	Status  *Status
	Version int
	// AnyVersion skips the version check (batch operations).
	AnyVersion bool
	// DisabledReason / DisabledUntil are stored when Status is disabled and
	// cleared when the user ends up active.
	DisabledReason string
	DisabledUntil  *time.Time
}

// IdentityDetail is a linked identity as administrators see it.
type IdentityDetail struct {
	Provider  string    `json:"provider"`
	Subject   string    `json:"subject"`
	Email     *string   `json:"email"`
	CreatedAt time.Time `json:"createdAt"`
}

type ListUsersQuery struct {
	Q string
	// GroupID filters by group (nil = all).
	GroupID *uuid.UUID
	Sort    string // "createdAt", "-createdAt", "lastLoginAt", "-lastLoginAt", "displayName"
	Offset  int
	Limit   int
}
