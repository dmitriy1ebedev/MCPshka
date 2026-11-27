package mcpserver

import (
	"fmt"
	"mcpshka/internal/readiness"
	"strings"
	"testing"
)

func TestBootstrapBackendHealthy(t *testing.T) {
	for _, c := range []struct {
		counts map[string]int
		want   string
	}{
		{map[string]int{readiness.Ready: 3, readiness.Registered: 9}, "true"},
		{map[string]int{readiness.Ready: 3, readiness.Preflight: 1}, "unknown"},
		{map[string]int{readiness.Registered: 9}, "unknown"},
		{map[string]int{readiness.Ready: 3, readiness.Blocked: 1}, "false"},
	} {
		if got := bootstrapBackendHealthy(c.counts); got != c.want {
			t.Errorf("%v: got %s want %s", c.counts, got, c.want)
		}
	}
}

func TestBootstrapReadinessReportsFirstBackendBlocker(t *testing.T) {
	out := CapabilityBootstrap{BackendHealthy: "false", RegisteredCount: 333}
	out.SetBootstrapReadiness([]CapabilityReadiness{
		{Name: "a_pending", State: readiness.Preflight, Reason: "no evidence"},
		{Name: "b_blocked", State: readiness.Blocked, Reason: "required worker unavailable"},
		{Name: "c_degraded", State: readiness.Degraded, Reason: "cache unavailable"},
	})
	if out.Ready || out.Blocker != "b_blocked: required worker unavailable" || out.NextSafeAction == nil || out.NextSafeAction.Tool != "capability_selftest" {
		t.Fatalf("%+v", out)
	}
	c := fullCatalogClient(t)
	boot := catalogCall(t, c, "capabilities_bootstrap", map[string]any{})
	ready, ok := boot["ready"].(bool)
	if !ok || ready != (boot["backend_healthy"] == "true") || !ready && (boot["blocker"] == nil || boot["next_safe_action"] == nil) {
		t.Fatalf("inconsistent wire readiness: %v", boot)
	}
	out2 := catalogCall(t, c, "tool_resolve", map[string]any{"goal": "git_push"})
	if !strings.Contains(fmt.Sprint(out2["candidates"]), "git_provider_push") {
		t.Fatalf("git_push did not resolve: %v", out2)
	}
}

func TestPreflightStopsAtUnregisteredCapability(t *testing.T) {
	r := &toolRegistry{}
	out, err := r.preflight(t.Context(), CapabilityPreflightInput{Step: "P1", Capabilities: []string{"no_such_tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != readiness.Blocked || len(out.Stages) != 1 || out.Stages[0].Stage != "capabilities" || out.NextSafeAction == nil {
		t.Fatalf("%+v", out)
	}
	if _, err := r.preflight(t.Context(), CapabilityPreflightInput{}); err == nil {
		t.Fatal("empty capability list accepted")
	}
}
