package notify

import (
	"testing"

	"github.com/google/uuid"

	"omnigate/internal/identity"
)

func TestPhase7EventCatalog(t *testing.T) {
	for _, role := range []identity.Role{identity.RoleUser, identity.RoleAuditor, identity.RoleSystemAdmin} {
		if !(Eligibility{Role: role}).Eligible(TypeAccountStatusChanged) {
			t.Fatalf("%s not eligible for account.status_changed", role)
		}
	}
	if !IsAlert(TypeAccountStatusChanged) || IsAlert(TypeSubscriptionQuotaReset) || IsAlert(TypeSubscriptionExtended) {
		t.Fatal("alert classes")
	}
	if !ReachesDisabled(TypeAccountStatusChanged) || ReachesDisabled(TypeSubscriptionExtended) || ReachesDisabled(TypeWalletCredited) {
		t.Fatal("only account events reach disabled users")
	}
	p := defaultStored(uuid.New())
	for _, typ := range []string{TypeAccountStatusChanged, TypeSubscriptionQuotaReset, TypeSubscriptionExtended} {
		if sw := p.switches(typ); !sw.Email || !sw.InApp || sw.Webhook {
			t.Fatalf("%s defaults = %+v", typ, sw)
		}
	}
	p.Events[TypeAccountStatusChanged] = Switches{}
	p.Events[TypeSubscriptionExtended] = Switches{}
	if sw := p.switches(TypeAccountStatusChanged); !sw.InApp || sw.Email {
		t.Fatalf("locked in-app = %+v", sw)
	}
	if sw := p.switches(TypeSubscriptionExtended); sw.InApp {
		t.Fatalf("extended in-app can be switched off: %+v", sw)
	}
	if Catalog[0].Type != TypeAccountStatusChanged {
		t.Fatal("account events come first")
	}
}
