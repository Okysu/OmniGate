package notify

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"omnigate/internal/identity"
)

// phase5-api.md §5.5: channel.share_invited reaches every user (recipients
// need not own channels), in-app only by default, and is not a channel alert.
func TestShareInvitedEvent(t *testing.T) {
	for _, role := range []identity.Role{identity.RoleUser, identity.RoleAuditor, identity.RoleSystemAdmin} {
		if !(Eligibility{Role: role}).Eligible(TypeChannelShareInvited) {
			t.Fatalf("%s not eligible for channel.share_invited", role)
		}
	}
	if IsAlert(TypeChannelShareInvited) || ReachesDisabled(TypeChannelShareInvited) {
		t.Fatal("channel.share_invited is neither an alert nor delivered to disabled users")
	}
	if sw := defaultStored(uuid.New()).switches(TypeChannelShareInvited); !sw.InApp || sw.Email || sw.Webhook {
		t.Fatalf("defaults = %+v", sw)
	}
	alerts := ChannelAlertTypes()
	if slices.Contains(alerts, TypeChannelShareInvited) || !slices.Contains(alerts, TypeChannelUnhealthy) || !slices.Contains(alerts, TypeUpstreamBalanceLow) {
		t.Fatalf("channel alert types = %v", alerts)
	}
}
