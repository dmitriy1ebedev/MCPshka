package issues

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInstalledVerificationClosesAndReopensOnlyItsOwnReceipt(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "fix")
	fix := git("rev-parse", "HEAD")[:40]
	git("commit", "-q", "--allow-empty", "-m", "release")
	head := git("rev-parse", "HEAD")[:40]
	s := NewStore(filepath.Join(t.TempDir(), "issues.json"))
	i, err := s.Create(NewIssue{Title: "Card with an installed check", Priority: "P1", Body: "Check the running release."}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(i.ID, Patch{Status: Fixed, Commit: fix, Release: "0.17.2"}, "test"); err != nil {
		t.Fatal(err)
	}
	proof := InstalledVerdict{ID: i.ID, Commit: fix, Release: "0.17.2", Head: head, Check: "fixture", Pass: true, Detail: "100%"}
	ctx := context.Background()
	if got, err := s.ReconcileInstalled(ctx, repo, proof); got != Closed || err != nil {
		t.Fatalf("close: %s %v", got, err)
	}
	proof.Pass, proof.Check = false, "other-check"
	if got, err := s.ReconcileInstalled(ctx, repo, proof); got != "" || err != nil {
		t.Fatalf("unrelated failure: %s %v", got, err)
	}
	proof.Check = "fixture"
	if got, err := s.ReconcileInstalled(ctx, repo, proof); got != Open || err != nil {
		t.Fatalf("reopen: %s %v", got, err)
	}
	i, err = s.Get(i.ID)
	if err != nil || i.Status != Open || i.Acceptance != nil {
		t.Fatalf("durable reopen: %+v %v", i, err)
	}
	proof.Commit, proof.Head = head, fix
	if _, err := s.ReconcileInstalled(ctx, repo, proof); err == nil {
		t.Fatal("fix outside the installed tree was accepted")
	}
}
