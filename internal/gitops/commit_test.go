package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/policy"
)

func TestCommitOnlyListedFiles(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) string {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Dir = root
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return string(out)
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)
	run("add", "a.txt")
	run("commit", "-qm", "seed")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a2"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0o644)
	run("add", "b.txt") // staged but not listed: must stay out
	s := New(&policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	res, err := s.Commit(root, []string{"a.txt"}, "update a")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commit) != 40 || !strings.Contains(res.Stat, "a.txt") || strings.Contains(res.Stat, "b.txt") {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(run("status", "--porcelain"), "b.txt") {
		t.Fatal("unlisted staged file was committed")
	}
	if _, err := s.Commit(root, []string{"../x"}, "m"); err == nil {
		t.Fatal("path outside the repo accepted")
	}
	if _, err := s.Commit(root, []string{"a.txt"}, "again"); err == nil {
		t.Fatal("commit without changes accepted")
	}
}

func TestMCP217CommitUsesLiteralPaths(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	for _, name := range []string{"a[1].txt", "a1.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-qm", "seed")
	for _, name := range []string{"a[1].txt", "a1.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := New(&policy.Policy{AllowedWorkdirs: []string{root}})
	if _, err := s.Commit(root, []string{"a[1].txt"}, "literal path"); err != nil {
		t.Fatal(err)
	}
	if got := git("show", "--format=", "--name-only", "HEAD"); got != "a[1].txt" {
		t.Fatalf("commit contains wrong paths: %q", got)
	}
}

// 14.11: a client without rm/raw git must be able to delete a file, and a
// file already removed (worktree or index) commits as a deletion.
func TestCommitDeletesFiles(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) string {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Dir = root
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return string(out)
	}
	run("init", "-q")
	for _, f := range []string{"README.md", "old.txt", "gone.txt", "keep.txt"} {
		os.WriteFile(filepath.Join(root, f), []byte(f), 0o644)
	}
	run("add", "-A")
	run("commit", "-qm", "seed")
	os.Remove(filepath.Join(root, "old.txt")) // removed in the worktree only
	run("rm", "-q", "gone.txt")               // removed and staged already
	s := New(&policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	res, err := s.CommitChanges(root, []string{"old.txt", "gone.txt"}, []string{"README.md"}, "drop docs")
	if err != nil {
		t.Fatal(err)
	}
	tree := run("ls-tree", "--name-only", "HEAD")
	if strings.TrimSpace(tree) != "keep.txt" || len(res.Commit) != 40 {
		t.Fatalf("tree after delete: %q", tree)
	}
	if _, err := os.Stat(filepath.Join(root, "README.md")); !os.IsNotExist(err) {
		t.Fatal("delete did not remove the file from the worktree")
	}
	if _, err := s.CommitChanges(root, nil, []string{"keep.txt/.."}, "bad"); err == nil {
		t.Fatal("unclean delete path accepted")
	}
}
