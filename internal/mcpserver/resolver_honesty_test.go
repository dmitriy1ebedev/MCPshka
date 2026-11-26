package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMissingResolverDoesNotRecommendUnrelatedTools(t *testing.T) {
	client := catalogClient(t, nil)
	raw, bad := callText(t, client, "tool_resolve", map[string]any{"goal": "cloud_dispatch", "compact": true})
	var out ToolResolveResult
	if bad || json.Unmarshal([]byte(raw), &out) != nil {
		t.Fatal("resolve failed", raw)
	}
	if out.Code != "CAPABILITY_MISSING" || out.Candidates == nil || len(out.Candidates) != 0 {
		t.Fatalf("missing capability suggests unrelated operations: %+v", out)
	}
}
func TestGatewayV1KeepsMCPErrorOutsideOpaqueEnvelope(t *testing.T) {
	raw, err := json.Marshal(ToolInvokeResult{Tool: "fixture", IsError: true, Content: []string{"refused"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\"is_error\"") {
		t.Fatal("standard MCP error flag changed the cached v1 output schema", string(raw))
	}
}

func TestMissingHostEnrollmentDoesNotBecomeAnExecutableTaskPlan(t *testing.T) {
	client := catalogClient(t, nil)
	for _, goal := range []string{"подключить новый VPS SSH Tailscale VPN", "enroll a new SSH host"} {
		out := catalogCall(t, client, "task_plan", map[string]any{"goal": goal})
		steps, _ := out["steps"].([]any)
		if out["state"] != "MISSING" || len(steps) != 1 {
			t.Fatal("missing enrollment became an executable plan", goal, out)
		}
		resolved := catalogCall(t, client, "tool_resolve", map[string]any{"goal": goal, "compact": true})
		step := steps[0].(map[string]any)
		if step["fallback"] != resolved["instructions"] {
			t.Fatal("planner discarded the precise missing-capability reason", step, resolved)
		}
		if step["missing"] != true || step["tool"] != nil && step["tool"] != "" || step["fallback"] == "" {
			t.Fatal("missing operation lost its exact recovery guidance", goal, step)
		}
	}
}
