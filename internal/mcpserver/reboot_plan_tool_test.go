package mcpserver

import "testing"

// MCP-111: "перезагрузить хост" now resolves to a real planning tool.
func TestRebootGoalResolvesToPlan(t *testing.T) {
	c := catalogClient(t, nil)
	r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": "перезагрузить хост", "compact": true})
	cands, _ := r["candidates"].([]any)
	if len(cands) == 0 || cands[0].(map[string]any)["name"] != "host_reboot_plan" {
		t.Fatalf("reboot goal: %v", r)
	}
}
