package workbenchruntime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"mcpshka/internal/failure"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
)

func TestDirtySealedWorkflowHasVerifiedNoEffect(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".mcpshka"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := projectprofile.Profile{Schema: projectprofile.SchemaV1, Name: "fixture", Match: projectprofile.Match{Module: "fixture"},
		Workflows: map[string]projectprofile.Workflow{"fast": {Command: "python3", Args: []string{"check.py"}, TimeoutSeconds: 60, OutputLimitBytes: 4096}}}
	raw, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(root, ".mcpshka", "profile.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if raw, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(raw))
		}
	}
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	access := policy.Default()
	access.AllowedWorkdirs = []string{root}
	_, err := WorkflowPlan(projectprofile.New(access), access, root, "fast")
	if !failure.IsNoEffect(err) {
		t.Fatal("read-only preflight turned into ambiguous effect", err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "dirty.txt")); err != nil || string(raw) != "preserve" {
		t.Fatal("preflight changed WIP", err)
	}
}
