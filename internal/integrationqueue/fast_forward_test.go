package integrationqueue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func integrationFixture(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	gitTest(t, dir, "config", "user.name", "Test")
	gitTest(t, dir, "config", "user.email", "test@example.invalid")
	gitTest(t, dir, "commit", "--allow-empty", "-m", "base")
	base := gitTest(t, dir, "rev-parse", "HEAD")
	gitTest(t, dir, "checkout", "-b", "feature")
	gitTest(t, dir, "commit", "--allow-empty", "-m", "candidate")
	return dir, base, gitTest(t, dir, "rev-parse", "HEAD")
}

func TestFastForwardUpdatesOnlyPinnedUnopenedTarget(t *testing.T) {
	dir, base, candidate := integrationFixture(t)
	out, err := applyFastForward(context.Background(), dir, "main", base, candidate, "release-test", nil)
	if err != nil || out.TargetHead != candidate || !out.Applied {
		t.Fatalf("integration: %+v %v", out, err)
	}
	if got := gitTest(t, dir, "rev-parse", "refs/heads/main"); got != candidate {
		t.Fatalf("target=%s", got)
	}
	if got := gitTest(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/feature" {
		t.Fatalf("source checkout changed: %s", got)
	}
}

func TestFastForwardRejectsCheckedOutTargetAndLinkedWorktree(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "linked"}[linked], func(t *testing.T) {
			dir, base, candidate := integrationFixture(t)
			if linked {
				gitTest(t, dir, "worktree", "add", filepath.Join(t.TempDir(), "target"), "main")
			} else {
				gitTest(t, dir, "checkout", "main")
			}
			if _, err := applyFastForward(context.Background(), dir, "main", base, candidate, "release-test", nil); err == nil {
				t.Fatal("checked-out target was updated")
			}
			if got := gitTest(t, dir, "rev-parse", "main"); got != base {
				t.Fatalf("target mutated: %s", got)
			}
		})
	}
}

func TestFastForwardCASRejectsTargetChangeAfterPlanning(t *testing.T) {
	dir, base, candidate := integrationFixture(t)
	other := gitTest(t, dir, "commit-tree", candidate+"^{tree}", "-p", base, "-m", "concurrent")
	_, err := applyFastForward(context.Background(), dir, "main", base, candidate, "release-test", func() error {
		gitTest(t, dir, "update-ref", "refs/heads/main", other, base)
		return nil
	})
	if err == nil || gitTest(t, dir, "rev-parse", "main") != other {
		t.Fatal("CAS overwrote concurrent writer")
	}
}

func TestFastForwardRejectsDivergenceSymbolicRefAndDirtySource(t *testing.T) {
	for _, mode := range []string{"diverged", "symbolic", "dirty"} {
		t.Run(mode, func(t *testing.T) {
			dir, base, candidate := integrationFixture(t)
			switch mode {
			case "diverged":
				other := gitTest(t, dir, "commit-tree", candidate+"^{tree}", "-p", base, "-m", "other")
				gitTest(t, dir, "update-ref", "refs/heads/main", other, base)
				base = other
			case "symbolic":
				gitTest(t, dir, "symbolic-ref", "refs/heads/main", "refs/heads/feature")
			case "dirty":
				if err := os.WriteFile(filepath.Join(dir, "unknown"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := applyFastForward(context.Background(), dir, "main", base, candidate, "release-test", nil); err == nil {
				t.Fatal("unsafe target/source accepted")
			}
		})
	}
}

func TestFastForwardReconcilesCompletedMutationWithoutUpdate(t *testing.T) {
	dir, base, candidate := integrationFixture(t)
	gitTest(t, dir, "update-ref", "refs/heads/main", candidate, base)
	called := false
	out, err := applyFastForward(context.Background(), dir, "main", base, candidate, "release-test", func() error {
		called = true
		return errors.New("mutation must not repeat")
	})
	if err != nil || called || out.Applied || out.TargetHead != candidate {
		t.Fatalf("recovery repeated Git mutation: %+v called=%v err=%v", out, called, err)
	}
}

func TestFastForwardReceiptPreventsABADuplicate(t *testing.T) {
	dir, base, candidate := integrationFixture(t)
	if _, err := applyFastForward(context.Background(), dir, "main", base, candidate, "release-aba", nil); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "update-ref", "refs/heads/main", base, candidate)
	if _, err := applyFastForward(context.Background(), dir, "main", base, candidate, "release-aba", nil); err == nil {
		t.Fatal("completed execution replayed after target ABA")
	}
	if gitTest(t, dir, "rev-parse", "main") != base {
		t.Fatal("target mutated twice")
	}
}

func TestFastForwardAttemptIsDurableBeforeTargetEffect(t *testing.T) {
	dir, base, candidate := integrationFixture(t)
	_, err := applyFastForward(context.Background(), dir, "main", base, candidate, "release-intent", func() error {
		if got := gitTest(t, dir, "rev-parse", "refs/mcpshka/integration/"+runtimeDigest("release-intent")); got != candidate {
			t.Fatalf("attempt marker missing before effect: %s", got)
		}
		return errors.New("crash before target update")
	})
	if err == nil {
		t.Fatal("crash missing")
	}
	if _, err := applyFastForward(context.Background(), dir, "main", base, candidate, "release-intent", nil); err == nil {
		t.Fatal("ambiguous attempt replayed")
	}
	if gitTest(t, dir, "rev-parse", "main") != base {
		t.Fatal("target changed after unknown attempt")
	}
}
