package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

// MCP-065/084/087: calls are counted per tool and route without payload.
func TestToolUsageCountsRoutesWithoutArguments(t *testing.T) {
	c := catalogClient(t, nil)
	catalogCall(t, c, "tool_resolve", map[string]any{"goal": "secret-goal-text перезапусти контейнер", "compact": true})
	catalogCall(t, c, "tool_invoke", map[string]any{"name": "tool_resolve", "arguments": map[string]any{"goal": "логи службы", "compact": true}})
	out := catalogCall(t, c, "tool_usage", map[string]any{"spans": 50})
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "secret-goal-text") {
		t.Fatal("telemetry leaked call arguments")
	}
	var found map[string]any
	for _, u := range out["tools"].([]any) {
		if m := u.(map[string]any); m["tool"] == "tool_resolve" {
			found = m
		}
	}
	if found == nil || found["calls"].(float64) < 2 || found["compat_calls"].(float64) < 1 {
		t.Fatalf("tool_resolve usage: %v", found)
	}
	if out["never_observed"].(float64) < 1 || out["window_started_at"] == "" {
		t.Fatalf("window/never: %v", out)
	}
}
