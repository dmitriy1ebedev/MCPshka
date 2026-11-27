package mission

import (
	"testing"
	"time"
)

// TestTrimKeepsBlockersAndKnownState: more observations than the list shows
// must not turn the report unknown, and the cut keeps the blocker (MCP-169).
func TestTrimKeepsBlockersAndKnownState(t *testing.T) {
	now := time.Now()
	obs := []Observation{}
	for i := 0; i < 30; i++ {
		obs = append(obs, Observation{Kind: "lease", ID: "l", Path: "/root/repos/p", Phase: "held", EvidenceAt: now.Add(-time.Minute)})
	}
	obs = append(obs, Observation{Kind: "agent_task", ID: "x", Path: "/root/repos/q", Phase: "failed", EvidenceAt: now.Add(-time.Hour)})
	r := Evaluate(now, obs, nil, true)
	r.Trim(16)
	if r.State != "degraded" || len(r.Items) != 16 || r.Omitted != 15 || r.Items[0].ID != "x" {
		t.Fatalf("state=%s items=%d omitted=%d first=%s", r.State, len(r.Items), r.Omitted, r.Items[0].ID)
	}
}
