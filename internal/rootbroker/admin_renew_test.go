package rootbroker

import (
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// A renewed admin challenge stays bound to its generation: after close or
// revoke (generation bump) or while a session is active it never renews.
func TestAdminRenewBoundToGeneration(t *testing.T) {
	p := passkeySession{Generation: 17, Data: webauthn.SessionData{UserVerification: protocol.VerificationRequired}}
	if err := adminRenewAllowed(p, AdminSession{Generation: 17, State: "not_found"}); err != nil {
		t.Fatalf("same generation, nothing active: %v", err)
	}
	for name, current := range map[string]AdminSession{
		"superseded": {Generation: 18, State: "not_found"},
		"active":     {Generation: 17, State: "active"},
	} {
		if err := adminRenewAllowed(p, current); err == nil {
			t.Fatalf("%s challenge renewed", name)
		}
	}
	p.Cancelled = true
	if err := adminRenewAllowed(p, AdminSession{Generation: 17}); err == nil {
		t.Fatal("cancelled challenge renewed")
	}
}
