package toolcontract

import "testing"

// MCP-130: issue_update/accept/dispatch probe the card itself; without the
// id the before-probe answered «issue not found» and held every change.
func TestIssueProbesCarryCardID(t *testing.T) {
	for _, name := range []string{"issue_update", "issue_accept", "issue_dispatch"} {
		c := Contracts[name]
		for _, before := range []bool{true, false} {
			probe := c.Probe
			if !before {
				probe = c.Recovery
			}
			got, args, err := ProbeArguments(name, probe, map[string]any{"id": "MCP-130", "idempotency_key": "k"}, map[string]any{}, before)
			if err != nil || got != "issue_get" || args["id"] != "MCP-130" {
				t.Fatalf("%s before=%v: probe %q args %v err %v", name, before, got, args, err)
			}
		}
	}
}
