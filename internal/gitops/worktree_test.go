package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"mcpshka/internal/policy"
)

func TestWorktreeCreateListRemove(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(root, 0o755)
	run := func(dir string, args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run(root, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)
	run(root, "add", "a.txt")
	run(root, "commit", "-qm", "seed")
	old := WorktreeRoot
	WorktreeRoot = filepath.Join(t.TempDir(), "wt")
	defer func() { WorktreeRoot = old }()
	s := New(&policy.Policy{AllowedWorkdirs: []string{filepath.Dir(root), WorktreeRoot}, DeniedCommands: map[string]struct{}{}})
	if _, err := s.Worktree(WorktreeInput{Op: "create", Path: root, Branch: "feat/x"}); err == nil {
		t.Fatal("created without a worktree root")
	}
	os.MkdirAll(WorktreeRoot, 0o755)
	for _, bad := range []string{"main", "Feat", "a..b", "-x"} {
		if _, err := s.Worktree(WorktreeInput{Op: "create", Path: root, Branch: bad}); err == nil {
			t.Fatalf("branch %q accepted", bad)
		}
	}
	res, err := s.Worktree(WorktreeInput{Op: "create", Path: root, Branch: "feat/x"})
	if err != nil || res.Worktree.Path != filepath.Join(WorktreeRoot, "proj-feat-x") || len(res.Worktree.Head) != 40 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err = s.Worktree(WorktreeInput{Op: "create", Path: root, Branch: "feat/x"}); err == nil {
		t.Fatal("existing branch recreated")
	}
	if _, err = s.Worktree(WorktreeInput{Op: "create", Path: root, Branch: "feat/y", Base: "nope"}); err == nil {
		t.Fatal("bad base accepted")
	}
	if c := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/heads/feat/y"); c.Run() == nil {
		t.Fatal("failed create left its branch")
	}
	list, _ := s.Worktree(WorktreeInput{Op: "list", Path: root})
	if len(list.Worktrees) != 2 || list.Worktrees[1].Branch != "feat/x" {
		t.Fatalf("%+v", list)
	}
	os.WriteFile(filepath.Join(res.Worktree.Path, "a.txt"), []byte("dirty"), 0o644)
	if _, err = s.Worktree(WorktreeInput{Op: "remove", Path: root, Branch: "feat/x"}); err == nil {
		t.Fatal("dirty worktree removed")
	}
	run(res.Worktree.Path, "checkout", "--", "a.txt")
	if _, err = s.Worktree(WorktreeInput{Op: "remove", Path: root, Branch: "feat/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(res.Worktree.Path); !os.IsNotExist(err) {
		t.Fatal("checkout left behind")
	}
	if c := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/heads/feat/x"); c.Run() != nil {
		t.Fatal("branch dropped on remove")
	}
}
