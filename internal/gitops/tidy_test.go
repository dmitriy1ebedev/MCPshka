package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/policy"
)

// After a release only work in flight survives: merged clean worktrees and
// merged branches go; unmerged, dirty and leased checkouts stay.
func TestTidyMergedKeepsOnlyLiveWork(t *testing.T) {
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
	os.MkdirAll(WorktreeRoot, 0o755)
	s := New(&policy.Policy{AllowedWorkdirs: []string{filepath.Dir(root), WorktreeRoot}, DeniedCommands: map[string]struct{}{}})
	wt := map[string]string{}
	for _, b := range []string{"done", "dirty", "leased", "open"} {
		res, err := s.Worktree(WorktreeInput{Op: "create", Path: root, Branch: "feat/" + b})
		if err != nil {
			t.Fatal(err)
		}
		wt[b] = res.Worktree.Path
	}
	os.WriteFile(filepath.Join(wt["dirty"], "a.txt"), []byte("wip"), 0o644)
	os.WriteFile(filepath.Join(wt["open"], "b.txt"), []byte("b"), 0o644)
	run(wt["open"], "add", "b.txt")
	run(wt["open"], "commit", "-qm", "not shipped")
	run(root, "branch", "old/merged")
	notes := strings.Join(s.TidyMerged(root, func(dir string) string {
		if dir == wt["leased"] {
			return "chat-1"
		}
		return ""
	}), "\n")
	for _, b := range []string{"dirty", "leased", "open"} {
		if _, err := os.Stat(wt[b]); err != nil {
			t.Fatalf("%s removed: %s", b, notes)
		}
	}
	if _, err := os.Stat(wt["done"]); err == nil || !strings.Contains(notes, "lease held by chat-1") {
		t.Fatalf("merged clean worktree must go: %s", notes)
	}
	branches, _ := exec.Command("git", "-C", root, "branch", "--format=%(refname:short)").Output()
	got := strings.Fields(string(branches))
	if strings.Contains(string(branches), "feat/done") || strings.Contains(string(branches), "old/merged") || len(got) != 4 {
		t.Fatalf("merged branches must go, live ones stay: %v\n%s", got, notes)
	}
}
