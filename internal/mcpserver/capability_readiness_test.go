package mcpserver

import (
	"mcpshka/internal/readiness"
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
