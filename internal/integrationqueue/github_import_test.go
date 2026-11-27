package integrationqueue

import (
	"context"
	"testing"
)

func TestPrepareGitHubImportOnlyStagesFastForward(t *testing.T) {
	_, req, _ := runtimeFixture(t)
	ctx := context.Background()
	repo, candidate, base := req.Path, req.Commit, req.TargetHead
	gitTest(t, repo, "checkout", "main")
	gitTest(t, repo, "merge", "--no-ff", "--no-edit", "feature")
	merge := gitTest(t, repo, "rev-parse", "HEAD")
	gitTest(t, repo, "checkout", "feature")
	gitTest(t, repo, "update-ref", "refs/heads/main", base, merge)
	plan, err := PrepareGitHubImport(ctx, repo, merge)
	if err != nil || !plan.FastForward || plan.TargetHead != base {
		t.Fatalf("import = %+v %v", plan, err)
	}
	_, branch := GitHubImportPins(merge)
	if got := gitTest(t, repo, "rev-parse", "refs/heads/"+branch); got != merge {
		t.Fatalf("staged %s, want %s", got, merge)
	}
	if _, err := PrepareGitHubImport(ctx, repo, merge); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := gitTest(t, repo, "rev-parse", "refs/heads/main"); got != base {
		t.Fatalf("staging advanced main to %s", got)
	}
	gitTest(t, repo, "checkout", "main")
	gitTest(t, repo, "commit", "--allow-empty", "-m", "diverged main")
	if plan, err := PrepareGitHubImport(ctx, repo, merge); err == nil || !plan.Diverged {
		t.Fatalf("diverged import = %+v %v", plan, err)
	}
	req.ID, req.Branch = GitHubImportPins(candidate)
	if VerifyGitHubImportApproval(ctx, req) == nil {
		t.Fatal("root approval accepted foreign repo")
	}
}
