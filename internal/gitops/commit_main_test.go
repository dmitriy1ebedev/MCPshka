package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/policy"
)

// BUG-20261104-36: MCPshka main moves only through integration; a work
// branch of the same checkout still commits.
func TestCommitRefusesIntegratedMain(t *testing.T) {
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	old := integratedRoot
	integratedRoot = root
	t.Cleanup(func() { integratedRoot = old })
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)
	run("add", "a.txt")
	run("commit", "-qm", "seed")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a2"), 0o644)
	s := New(&policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if _, err := s.Commit(root, []string{"a.txt"}, "direct"); err == nil || !strings.Contains(err.Error(), "integration_ship") {
		t.Fatalf("commit on integrated main: %v", err)
	}
	run("switch", "-q", "-c", "release/x")
	if _, err := s.Commit(root, []string{"a.txt"}, "on branch"); err != nil {
		t.Fatal(err)
	}
}
