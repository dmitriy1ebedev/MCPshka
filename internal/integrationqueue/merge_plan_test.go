package integrationqueue

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanMerge(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	gitTest(t, dir, "config", "user.name", "Test")
	gitTest(t, dir, "config", "user.email", "test@example.invalid")
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "file")
	gitTest(t, dir, "commit", "-m", "base")
	base := gitTest(t, dir, "rev-parse", "HEAD")
	gitTest(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(file, []byte("feature"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "commit", "-am", "feature")
	candidate := gitTest(t, dir, "rev-parse", "HEAD")
	p, err := PlanMerge(context.Background(), dir, "main", candidate)
	if err != nil || !p.FastForward || p.TargetHead != base {
		t.Fatalf("fast-forward: %+v %v", p, err)
	}
	gitTest(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "other"), []byte("main"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "other")
	gitTest(t, dir, "commit", "-m", "main update")
	p, err = PlanMerge(context.Background(), dir, "main", candidate)
	if err != nil || !p.Diverged || p.FastForward {
		t.Fatalf("divergence: %+v %v", p, err)
	}
	if _, err = PlanMerge(context.Background(), dir, "../main", candidate); err == nil {
		t.Fatal("unsafe ref accepted")
	}
}

func TestPlanMergeRejectsMissingAncestryObject(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	gitTest(t, dir, "config", "user.name", "Test")
	gitTest(t, dir, "config", "user.email", "test@example.invalid")
	gitTest(t, dir, "commit", "--allow-empty", "-m", "base")
	base := gitTest(t, dir, "rev-parse", "HEAD")
	gitTest(t, dir, "checkout", "-b", "feature")
	gitTest(t, dir, "commit", "--allow-empty", "-m", "candidate")
	candidate := gitTest(t, dir, "rev-parse", "HEAD")
	gitTest(t, dir, "checkout", "main")
	gitTest(t, dir, "commit", "--allow-empty", "-m", "target")
	if err := os.Remove(filepath.Join(dir, ".git", "objects", base[:2], base[2:])); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanMerge(context.Background(), dir, "main", candidate)
	if err == nil || plan.FastForward || plan.Diverged || plan.AlreadyMerged {
		t.Fatalf("failed Git probe authorized integration: %+v %v", plan, err)
	}
}
