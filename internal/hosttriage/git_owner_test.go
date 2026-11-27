package hosttriage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitOwnerProblemsQuietOnAConsistentRepo(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "p", ".git", "refs", "remotes", "origin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "p", ".git", "refs", "remotes", "origin", "main"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := gitOwnerProblems(root); len(p) != 0 {
		t.Fatalf("one owner flagged: %+v", p)
	}
	// A foreign owner needs root to create; on a root test run chown proves it.
	if os.Geteuid() != 0 {
		return
	}
	if err := os.Chown(filepath.Join(root, "p", ".git"), 1234, 1234); err != nil {
		// uid 0 without CAP_CHOWN: the sealed suite runs under bwrap --cap-drop ALL
		// (0.19.0 sealed attempts 1 and 2 failed here).
		t.Skipf("no CAP_CHOWN: %v", err)
	}
	p := gitOwnerProblems(root)
	if len(p) != 1 || p[0].Kind != "git-ownership" {
		t.Fatalf("root-owned refs under a 1234 .git not flagged: %+v", p)
	}
}
