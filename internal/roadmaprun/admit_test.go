package roadmaprun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// MCP-100: an agent refused by admission is skipped without spending an
// attempt; with no admitted agent the run stops with an admission HOLD.
func TestRunnerSkipsUnadmittedAgent(t *testing.T) {
	withStateDir(t)
	roadmap := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(roadmap, []byte("## P09 — x\n- [ ] a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var used []string
	agent := func(ctx context.Context, name, dir, prompt string) (string, error) {
		used = append(used, name)
		return "PHASE_DONE", nil
	}
	gate := func(ctx context.Context, path, phase string) (string, string, string, error) {
		return "PASS", strings.Repeat("a", 40), "proceed", nil
	}
	refuse := map[string]bool{"codex": true}
	admit := func(ctx context.Context, a string) (bool, string) {
		if refuse[a] {
			return false, "not logged in"
		}
		return true, ""
	}
	cfg := Config{ID: "r9", Path: "/root/repos/diag", Roadmap: roadmap, MaxPhases: 1, Budget: time.Hour, Agents: []string{"codex", "claude"}, RunAgent: agent, Gate: gate, Admit: admit}
	j, err := Run(context.Background(), cfg, "rules")
	if err != nil || j.State != "done" || len(used) != 1 || used[0] != "claude" {
		t.Fatalf("fallback: %+v used=%v %v", j, used, err)
	}
	if err := os.WriteFile(roadmap, []byte("## P10 — y\n- [ ] b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	refuse["claude"], used = true, nil
	cfg.ID = "r10"
	j, _ = Run(context.Background(), cfg, "rules")
	if j.State != "stopped" || len(used) != 0 || !strings.Contains(j.StopReason, "admission HOLD") || !strings.Contains(j.StopReason, "codex: not logged in") {
		t.Fatalf("all refused: %+v used=%v", j, used)
	}
}
