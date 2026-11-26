package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/policy"
)

func makeRepo(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = policy.MinimalEnv(root)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "initial")
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	return New(p), root
}

func TestStatusAndRepoRoot(t *testing.T) {
	s, root := makeRepo(t)
	nested := filepath.Join(root, "sub")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	gotRoot, err := s.RepoRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root {
		t.Fatalf("root=%q want=%q", gotRoot, root)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(root, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty || len(status.Entries) == 0 {
		t.Fatalf("expected dirty status: %+v", status)
	}
	found := false
	for _, line := range status.Entries {
		if strings.Contains(line, "new.txt") {
			found = true
		}
	}
	if !found {
		t.Fatalf("new.txt missing from status: %+v", status)
	}
}

func TestDiffIsBounded(t *testing.T) {
	s, root := makeRepo(t)
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString("changed line with enough text to make the diff large\n")
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	diff, err := s.Diff(root, false, 3, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Text) > 1024 {
		t.Fatalf("diff exceeded cap: %d", len(diff.Text))
	}
	if !diff.Truncated || diff.TotalBytes <= 1024 {
		t.Fatalf("expected truncated diff: %+v", diff)
	}
}

func TestRepoRootDoesNotEscapeAllowedRoot(t *testing.T) {
	outer := t.TempDir()
	repo := filepath.Join(outer, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", outer)
	cmd.Env = policy.MinimalEnv(outer)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{repo}, DeniedCommands: map[string]struct{}{}}
	s := New(p)
	if _, err := s.RepoRoot(repo); err == nil {
		t.Fatal("expected repository outside allowed root to be rejected")
	}
}

func TestCommonDirIsSharedBySiblingWorktrees(t *testing.T) {
	main := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(main, "init", "-q", "-b", "main")
	run(main, "commit", "-q", "--allow-empty", "-m", "base")
	wt := filepath.Join(t.TempDir(), "wt")
	run(main, "worktree", "add", "-q", wt)
	a, err := CommonDir(main)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CommonDir(wt)
	if err != nil || a != b || a != filepath.Join(main, ".git") {
		t.Fatalf("main %q, worktree %q, %v", a, b, err)
	}
}
