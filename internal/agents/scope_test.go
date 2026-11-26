package agents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestScopeArgs(t *testing.T) {
	if got, err := ScopeArgs("/root/repos/diag/"); err != nil || len(got) != 1 || got[0] != "/root/repos/diag" {
		t.Fatalf("repo: %v %v", got, err)
	}
	if _, err := ScopeArgs("/root/repos/wt/not-a-worktree-" + filepath.Base(t.TempDir())); err == nil {
		t.Fatal("a missing worktree must be refused")
	}
	// A live worktree of this checkout, when the test runs inside one.
	wd, _ := os.Getwd()
	root := filepath.Dir(filepath.Dir(wd))
	if got, err := ScopeArgs(root); err == nil && len(got) == 2 && got[1] != "/root/repos/mcpshka" {
		t.Fatalf("worktree: %v", got)
	}
}

func TestOwnWorktree(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "proj")
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("init", "-q", repo)
	run("-C", repo, "commit", "-q", "--allow-empty", "-m", "base")
	old := WorktreeRoot
	WorktreeRoot = filepath.Join(dir, "wt")
	defer func() { WorktreeRoot = old }()
	path, err := OwnWorktree(context.Background(), repo, "0123456789abcdef")
	if err != nil || path != filepath.Join(WorktreeRoot, "proj-agent-0123456789abcdef") {
		t.Fatalf("%q %v", path, err)
	}
	out, _ := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if string(out) != "agent/0123456789abcdef\n" {
		t.Fatalf("branch %q", out)
	}
	if _, err := OwnWorktree(context.Background(), path, "0123456789abcdef"); err == nil {
		t.Fatal("a worktree is not a repository")
	}
}
