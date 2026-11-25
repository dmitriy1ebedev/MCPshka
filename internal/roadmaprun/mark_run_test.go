package roadmaprun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// MCP-004: an agent that forgets to tick ROADMAP no longer stops the run.
func TestRunClosesPassedPhaseInRoadmapViaCAS(t *testing.T) {
	withStateDir(t)
	roadmap := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(roadmap, []byte("## P03 — exec\n- [ ] a\n- [-] a2\n## P04 — schema\n- [ ] b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	agent := func(ctx context.Context, name, dir, prompt string) (string, error) { return "PHASE_DONE", nil }
	gate := func(ctx context.Context, path, phase string) (string, string, string, error) {
		return "PASS", strings.Repeat("a", 40), "proceed", nil
	}
	j, err := Run(context.Background(), Config{Agents: []string{"codex", "claude"}, ID: "r4", Path: "/root/repos/diag", Roadmap: roadmap, MaxPhases: 2, Budget: time.Hour, RunAgent: agent, Gate: gate}, "rules")
	if err != nil || j.State != "done" || len(j.Phases) != 2 || j.Phases[1].ID != "P04" {
		t.Fatalf("run: %+v %v", j, err)
	}
	if !strings.Contains(j.Phases[0].Note, "ROADMAP closed 2 item(s)") {
		t.Fatalf("note: %q", j.Phases[0].Note)
	}
	raw, _ := os.ReadFile(roadmap)
	if strings.Contains(string(raw), "[ ]") || strings.Contains(string(raw), "[-]") {
		t.Fatalf("roadmap not closed: %q", raw)
	}
}
