package authz

import (
	"testing"

	"github.com/google/uuid"

	"omnigate/internal/identity"
)

func TestRolePermissions(t *testing.T) {
	admin := &Principal{Role: identity.RoleSystemAdmin}
	user := &Principal{Role: identity.RoleUser}
	auditor := &Principal{Role: identity.RoleAuditor}
	var nobody *Principal

	if !admin.Can(UsersWrite) || !admin.Can(BillingManage) {
		t.Error("system admin must manage users and billing")
	}
	if user.Can(UsersRead) || user.Can(BillingManage) || user.Can(AuditRead) {
		t.Error("plain user must not read users, audit or manage billing")
	}
	if !user.Can(KeysOwn) || !user.Can(BillingOwn) {
		t.Error("plain user must own keys and billing")
	}
	if auditor.Can(KeysOwn) || auditor.Can(UsersWrite) || !auditor.Can(AuditRead) {
		t.Error("auditor is read-only")
	}
	if nobody.Can(ChannelsRead) {
		t.Error("nil principal must have no permissions")
	}
	if (&Principal{Role: identity.Role("bogus")}).Can(ChannelsRead) {
		t.Error("unknown roles must have no permissions")
	}
}

func TestCanOnResource(t *testing.T) {
	owner, other, friend := uuid.New(), uuid.New(), uuid.New()
	shared := func(id uuid.UUID, _ identity.Role) bool { return id == friend }
	priv := Resource{OwnerID: owner, Scope: ScopePrivate}
	shr := Resource{OwnerID: owner, Scope: ScopeShared, SharedTo: shared}
	glob := Resource{OwnerID: owner, Scope: ScopeGlobal}

	o := &Principal{UserID: owner, Role: identity.RoleUser}
	x := &Principal{UserID: other, Role: identity.RoleUser}
	f := &Principal{UserID: friend, Role: identity.RoleUser}
	ca := &Principal{UserID: uuid.New(), Role: identity.RoleChannelAdmin}

	cases := []struct {
		name string
		p    *Principal
		a    Action
		r    Resource
		want bool
	}{
		{"owner manages private", o, ActionManage, priv, true},
		{"stranger cannot use private", x, ActionUse, priv, false},
		{"friend uses shared", f, ActionUse, shr, true},
		{"friend cannot view shared config", f, ActionView, shr, false},
		{"stranger cannot use shared", x, ActionUse, shr, false},
		{"anyone uses global", x, ActionUse, glob, true},
		{"global does not grant manage", x, ActionManage, glob, false},
		{"channel admin manages others", ca, ActionManage, priv, true},
	}
	for _, c := range cases {
		if got := c.p.CanOnResource(c.a, c.r, ChannelsManage); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
