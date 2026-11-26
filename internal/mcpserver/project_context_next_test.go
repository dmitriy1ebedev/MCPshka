package mcpserver

import (
	"strings"
	"testing"
)

// MCP-036: project_context gives the exact next call, not only warning codes.
func TestProjectContextWithoutProfileGivesOnboardAction(t *testing.T) {
	out := ProjectContext{Root: "/root/repos/x", Warnings: []string{"reusable_profile_unavailable", "test_strategy_unavailable", "roadmap_missing"}}
	next := contextNextActions("/root/repos/x/sub/", out)
	joined := strings.Join(next, "\n")
	for _, want := range []string{"project_onboard_preview path=/root/repos/x", "path=/root/repos/x (requested /root/repos/x/sub)", "no ROADMAP.md at /root/repos/x"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	if strings.Count(joined, "project_onboard_preview") != 1 {
		t.Fatalf("duplicate onboard action: %q", joined)
	}
	if got := contextNextActions("/root/repos/x", ProjectContext{Root: "/root/repos/x"}); len(got) != 0 {
		t.Fatalf("healthy context needs no action: %q", got)
	}
}
