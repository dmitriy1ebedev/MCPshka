package mcpserver

import "testing"

func TestOwnerPortalKeepsLegacyReadinessWithoutEventStore(t *testing.T) {
	c := catalogClient(t, nil)
	out := catalogCall(t, c, "owner_portal", map[string]any{})
	if _, ok := out["events"].([]any); !ok {
		t.Fatalf("events missing: %+v", out)
	}
	if out["events_state"] != "UNKNOWN" {
		t.Fatalf("unavailable state claimed observed: %+v", out)
	}
	if _, ok := out["indicators"].([]any); !ok {
		t.Fatal("readiness lost")
	}
}
