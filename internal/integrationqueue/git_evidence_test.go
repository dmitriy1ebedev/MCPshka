package integrationqueue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func TestObserveGitEvidence(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "review")
	gitTest(t, dir, "config", "user.name", "Review Test")
	gitTest(t, dir, "config", "user.email", "review@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "file")
	gitTest(t, dir, "commit", "-m", "init")
	commit := gitTest(t, dir, "rev-parse", "HEAD")
	e, err := ObserveGitEvidence(context.Background(), dir, "review", commit)
	if err != nil {
		t.Fatal(err)
	}
	if !e.CandidateReachable || !e.CandidateMerged || !e.CleanTarget || e.TargetHead != commit {
		t.Fatalf("unexpected: %+v", e)
	}

	if err := os.WriteFile(filepath.Join(dir, "untracked"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	e, err = ObserveGitEvidence(context.Background(), dir, "review", commit)
	if err != nil || e.CleanTarget {
		t.Fatalf("untracked worktree accepted: %+v %v", e, err)
	}
	if err := os.Remove(filepath.Join(dir, "untracked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	e, err = ObserveGitEvidence(context.Background(), dir, "review", commit)
	if err != nil {
		t.Fatal(err)
	}
	if e.CleanTarget {
		t.Fatal("dirty worktree accepted")
	}
	if _, err = ObserveGitEvidence(context.Background(), dir, "../review", commit); err == nil {
		t.Fatal("unsafe ref accepted")
	}
}
