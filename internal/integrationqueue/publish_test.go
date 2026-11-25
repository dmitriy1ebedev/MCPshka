package integrationqueue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func publishFixture(t *testing.T) (repo, base, candidate string, git func(...string) string) {
	t.Helper()
	repo = t.TempDir()
	git = func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Publish Test")
	git("config", "user.email", "publish@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-m", "base")
	base = git("rev-parse", "HEAD")
	git("checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "b.txt")
	git("commit", "-m", "candidate")
	candidate = git("rev-parse", "HEAD")
	git("checkout", "main")
	return repo, base, candidate, git
}

func TestPublishFastForwardsOnlyFromAdmittedTarget(t *testing.T) {
	repo, base, candidate, git := publishFixture(t)
	if err := os.WriteFile(filepath.Join(repo, "ROADMAP.md"), []byte("local untracked"), 0644); err != nil {
		t.Fatal(err)
	}
	state, err := PublishFastForward(context.Background(), repo, "main", base, candidate)
	if err != nil || state != "PUBLISHED" || git("rev-parse", "HEAD") != candidate {
		t.Fatal("publication failed", state, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatal("working tree not updated", err)
	}
	if state, err := PublishFastForward(context.Background(), repo, "main", base, candidate); err != nil || state != "ALREADY_PUBLISHED" {
		t.Fatal("repeat must be a no-op", state, err)
	}
}

func TestPublishHoldsOnDirtyMovedOrDivergedTarget(t *testing.T) {
	repo, base, candidate, git := publishFixture(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFastForward(context.Background(), repo, "main", base, candidate); err == nil {
		t.Fatal("dirty tracked checkout published")
	}
	git("checkout", "--", "a.txt")
	if err := os.WriteFile(filepath.Join(repo, "c.txt"), []byte("c"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "c.txt")
	git("commit", "-m", "someone else on main")
	if _, err := PublishFastForward(context.Background(), repo, "main", base, candidate); err == nil || git("rev-parse", "HEAD") == candidate {
		t.Fatal("moved target fast-forwarded")
	}
	moved := git("rev-parse", "HEAD")
	if _, err := PublishFastForward(context.Background(), repo, "main", moved, candidate); err == nil {
		t.Fatal("diverged candidate published")
	}
	git("checkout", "feature")
	if _, err := PublishFastForward(context.Background(), repo, "main", moved, candidate); err == nil {
		t.Fatal("published while checkout is on another branch")
	}
}

func TestPublishNamesFreshIndexLockAndClearsAbandonedOne(t *testing.T) {
	repo, base, candidate, git := publishFixture(t)
	lock := filepath.Join(repo, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := PublishFastForward(context.Background(), repo, "main", base, candidate)
	if err == nil || !strings.Contains(err.Error(), "index is locked") || git("rev-parse", "HEAD") != base {
		t.Fatal("fresh lock must hold with its reason", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("fresh lock must not be removed", err)
	}
	old := time.Now().Add(-staleIndexLockAge - time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	state, err := PublishFastForward(context.Background(), repo, "main", base, candidate)
	if err != nil || state != "PUBLISHED_STALE_LOCK_CLEARED" || git("rev-parse", "HEAD") != candidate {
		t.Fatal("abandoned lock blocked publication", state, err)
	}
}

func TestPublishHoldsBeforeTouchingAnUnwritableCheckout(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root passes access(2) for any directory")
	}
	repo, base, _, git := publishFixture(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sub, "x.txt"), []byte("x"), 0644)
	git("add", "sub/x.txt")
	git("commit", "-m", "base with sub")
	base = git("rev-parse", "HEAD")
	git("checkout", "-b", "owned")
	os.WriteFile(filepath.Join(sub, "c.txt"), []byte("c"), 0644)
	os.MkdirAll(filepath.Join(repo, "newdir"), 0755)
	os.WriteFile(filepath.Join(repo, "newdir", "d.txt"), []byte("d"), 0644)
	git("add", "sub/c.txt", "newdir/d.txt")
	git("commit", "-m", "candidate into a root-owned directory")
	candidate := git("rev-parse", "HEAD")
	git("checkout", "main")
	if err := os.Chmod(sub, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0755) })
	_, err := PublishFastForward(context.Background(), repo, "main", base, candidate)
	if err == nil || !strings.Contains(err.Error(), "sub/") || !strings.Contains(err.Error(), "HOLD") {
		t.Fatalf("unwritable directory not refused: %v", err)
	}
	if git("rev-parse", "HEAD") != base || git("status", "--porcelain") != "" {
		t.Fatal("checkout touched before the HOLD")
	}
	if _, err := os.Stat(filepath.Join(repo, "newdir")); !os.IsNotExist(err) {
		t.Fatal("new directory created before the HOLD")
	}
	os.Chmod(sub, 0755)
	if state, err := PublishFastForward(context.Background(), repo, "main", base, candidate); err != nil || state != "PUBLISHED" {
		t.Fatal("publish after fixing ownership", state, err)
	}
}

func TestPublishHoldsOnRootOwnedGitMetadata(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root passes access(2) for any file")
	}
	repo, base, candidate, git := publishFixture(t)
	for _, name := range []string{"index", "packed-refs"} {
		path := filepath.Join(repo, ".git", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			git("pack-refs", "--all")
		}
		if err := os.Chmod(path, 0444); err != nil {
			t.Fatal(err)
		}
		_, err := PublishFastForward(context.Background(), repo, "main", base, candidate)
		if err == nil || !strings.Contains(err.Error(), ".git/"+name) || !strings.Contains(err.Error(), "HOLD") {
			t.Fatalf("read-only %s not refused: %v", name, err)
		}
		if git("rev-parse", "HEAD") != base {
			t.Fatal("target moved before the HOLD")
		}
		os.Chmod(path, 0644)
	}
	if state, err := PublishFastForward(context.Background(), repo, "main", base, candidate); err != nil || state != "PUBLISHED" {
		t.Fatal("publish after fixing ownership", state, err)
	}
}

func TestPublishNamesUntrackedCollisionBeforeMerging(t *testing.T) {
	repo, base, candidate, git := publishFixture(t)
	// A local untracked copy where the candidate adds b.txt made the live
	// merge fail as "outcome UNKNOWN" (BUG-20261103-1).
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := PublishFastForward(context.Background(), repo, "main", base, candidate)
	if err == nil || !strings.Contains(err.Error(), "CHECKOUT_UNTRACKED_COLLISION") || !strings.Contains(err.Error(), "b.txt") || strings.Contains(err.Error(), "UNKNOWN") {
		t.Fatalf("collision not named: %v", err)
	}
	if git("rev-parse", "HEAD") != base {
		t.Fatal("checkout moved on a refused publish")
	}
	if raw, _ := os.ReadFile(filepath.Join(repo, "b.txt")); string(raw) != "b" {
		t.Fatal("local untracked file touched")
	}
	if err := os.Remove(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if state, err := PublishFastForward(context.Background(), repo, "main", base, candidate); err != nil || state != "PUBLISHED" {
		t.Fatal("publish after moving the copy aside", state, err)
	}
}

func TestPublishOffTargetHoldIsTyped(t *testing.T) {
	repo, base, candidate, git := publishFixture(t)
	git("checkout", "feature")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PublishFastForward(context.Background(), repo, "main", base, candidate)
	if err == nil || !strings.Contains(err.Error(), "CHECKOUT_NOT_ON_TARGET") || !strings.Contains(err.Error(), "git_status") {
		t.Fatalf("off-target HOLD not typed: %v", err)
	}
	if git("symbolic-ref", "HEAD") != "refs/heads/feature" {
		t.Fatal("dirty off-target checkout was switched")
	}
}

// The candidate branch left checked out after its commit no longer stops a
// release (owner, 08.11: every ship ended in CHECKOUT_NOT_ON_TARGET).
func TestPublishSwitchesCleanOffTargetCheckout(t *testing.T) {
	repo, base, candidate, git := publishFixture(t)
	git("checkout", "feature")
	if state, err := PublishFastForward(context.Background(), repo, "main", base, candidate); err != nil || state != "PUBLISHED" {
		t.Fatal("clean off-target publish", state, err)
	}
	if git("symbolic-ref", "HEAD") != "refs/heads/main" || git("rev-parse", "HEAD") != candidate {
		t.Fatal("checkout not on the published target")
	}
}
