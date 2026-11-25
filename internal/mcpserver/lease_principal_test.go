package mcpserver

import (
	"context"
	"testing"

	"mcpshka/internal/authn"
)

func TestLeaseReceiptsCannotCrossAuthenticatedPrincipals(t *testing.T) {
	a := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner-a", Role: "operator", Scopes: []string{"*"}})
	b := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner-b", Role: "operator", Scopes: []string{"*"}})
	input := map[string]any{"path": "/root/repos/example", "owner": "same-display-owner", "idempotency_key": "same-known-key"}
	for _, name := range []string{"project_lease_acquire", "project_lease_resume", "project_lease_release", "project_lease_heartbeat"} {
		if mutationReceiptKey(a, name, "same-known-key", input) == mutationReceiptKey(b, name, "same-known-key", input) {
			t.Error("lease receipt/credential can cross authenticated principal", name)
		}
	}
}
