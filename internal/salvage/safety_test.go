package salvage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPreviewRecognizesSquashedContentWithoutReplayingOldBranch(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a", "base", "base")
	git(t, repo, "checkout", "-q", "-b", "codex/squashed")
	commitFile(t, repo, "b", "one", "first")
	commitFile(t, repo, "c", "two", "second")
	git(t, repo, "checkout", "-q", "main")
	git(t, repo, "merge", "--squash", "codex/squashed")
	git(t, repo, "commit", "-q", "-m", "combined implementation")
	rep, err := Preview(context.Background(), repo, "main", []string{"codex/squashed"})
	if err != nil || len(rep.Candidates) != 1 || rep.Candidates[0].State != Superseded {
		t.Fatal("equivalent content suggested another integration", rep, err)
	}
}

func TestPreviewPreservesUntrackedWorkEvenOnIntegratedBranch(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a", "base", "base")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", "-b", "agent/untracked", wt, "main")
	if err := os.WriteFile(filepath.Join(wt, "valuable-wip"), []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := Preview(context.Background(), repo, "main", []string{"agent/untracked"})
	if err != nil || len(rep.Candidates) != 1 || rep.Candidates[0].State != WIP || !rep.Candidates[0].Dirty {
		t.Fatal("untracked work could be archived", rep, err)
	}
}

func TestPreviewNeverHidesObservationFailure(t *testing.T) {
	for _, fail := range []string{"worktree list", "status"} {
		t.Run(fail, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init", "-q", "-b", "main")
			commitFile(t, repo, "a", "base", "base")
			git(t, repo, "branch", "agent/existing", "main")
			wt := filepath.Join(t.TempDir(), "wt")
			git(t, repo, "worktree", "add", "-q", wt, "agent/existing")
			prior := Git
			t.Cleanup(func() { Git = prior })
			Git = func(ctx context.Context, repo string, args ...string) (string, error) {
				if strings.Contains(strings.Join(args, " "), fail) {
					return "", errors.New("observation unavailable")
				}
				return prior(ctx, repo, args...)
			}
			rep, err := Preview(context.Background(), repo, "main", []string{"agent/existing"})
			if err == nil && (len(rep.Candidates) != 1 || rep.Candidates[0].State != "unknown") {
				t.Fatal("failed observation looked safe", rep)
			}
		})
	}
}

func TestPreviewIncludesOtherLocalBranchFamiliesAndDoesNotClaimReleaseState(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a", "base", "base")
	git(t, repo, "checkout", "-q", "-b", "feature/valuable")
	commitFile(t, repo, "b", "new", "new")
	git(t, repo, "checkout", "-q", "main")
	rep, err := Preview(context.Background(), repo, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range rep.Candidates {
		if candidate.Branch == "feature/valuable" {
			if candidate.State != "needs_review" || strings.Contains(candidate.NextSafe, "then integration_ship from this branch") {
				t.Fatal("Git inventory fabricated release/merge acceptance", candidate)
			}
			return
		}
	}
	t.Fatal("local work disappeared from inventory", rep)
}

func TestPreviewDoesNotCallMergeResolutionSuperseded(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a", "base", "base")
	git(t, repo, "checkout", "-q", "-b", "codex/merge")
	commitFile(t, repo, "b", "feature", "feature")
	git(t, repo, "checkout", "-q", "main")
	commitFile(t, repo, "m", "main", "main advances")
	git(t, repo, "cherry-pick", "codex/merge")
	git(t, repo, "checkout", "-q", "codex/merge")
	git(t, repo, "merge", "--no-ff", "--no-commit", "main")
	if err := os.WriteFile(filepath.Join(repo, "resolution-only"), []byte("not in main"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "resolution-only")
	git(t, repo, "commit", "-q", "-m", "merge resolution")
	git(t, repo, "checkout", "-q", "main")
	rep, err := Preview(context.Background(), repo, "main", []string{"codex/merge"})
	if err != nil || len(rep.Candidates) != 1 || rep.Candidates[0].State != NeedsReview {
		t.Fatal("merge-only work hidden by patch-id", rep, err)
	}
}

func TestPreviewTreatsCandidateFilenamesAsLiteralPathspecs(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a", "base", "base")
	git(t, repo, "checkout", "-q", "-b", "codex/pathspec")
	name := ":(exclude)*"
	if err := os.WriteFile(filepath.Join(repo, name), []byte("unique candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "--literal-pathspecs", "add", "--", name)
	git(t, repo, "commit", "-q", "-m", "literal filename")
	git(t, repo, "checkout", "-q", "main")
	commitFile(t, repo, "m", "main", "main advances")
	rep, err := Preview(context.Background(), repo, "main", []string{"codex/pathspec"})
	if err != nil || len(rep.Candidates) != 1 || rep.Candidates[0].State != NeedsReview || rep.Candidates[0].ContentDifferent != 1 {
		t.Fatal("pathspec magic hid candidate content", rep, err)
	}
}

func TestGitObservationTimeoutStopsItsWholeChildGroup(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "git")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nsleep 5 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Git(ctx, dir, "status"); err == nil {
		t.Fatal("timed out observation accepted")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("descendant kept observation alive after its deadline")
	}
}

func TestGitObservationOutputBudget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nhead -c 300000 /dev/zero\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	if _, err := Git(context.Background(), dir, "status"); err == nil {
		t.Fatal("oversized observation accepted")
	}
}

func TestPreviewCannotArchiveTruncatedPatchEvidence(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a", "base", "base")
	git(t, repo, "checkout", "-q", "-b", "codex/truncated")
	commitFile(t, repo, "b", "feature", "feature")
	git(t, repo, "checkout", "-q", "main")
	commitFile(t, repo, "m", "main", "main advances")
	prior := Git
	t.Cleanup(func() { Git = prior })
	head, err := prior(context.Background(), repo, "rev-parse", "codex/truncated")
	if err != nil {
		t.Fatal(err)
	}
	Git = func(ctx context.Context, path string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "cherry" {
			return strings.Repeat("- "+strings.TrimSpace(head)+" equivalent\n", MaxCommits+1), nil
		}
		return prior(ctx, path, args...)
	}
	rep, err := Preview(context.Background(), repo, "main", []string{"codex/truncated"})
	if err != nil || len(rep.Candidates) != 1 || !rep.Candidates[0].Truncated || rep.Candidates[0].State != NeedsReview {
		t.Fatal("partial evidence authorized archival", rep, err)
	}
}

func TestPreviewPreservesWorktreeWithUntrustedCleanFilter(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a", "base", "base")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", "-b", "agent/filter", wt, "main")
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, []byte("[filter \"fixture\"]\n\tclean = touch FILTER_EXECUTED\n\trequired = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	if err := os.WriteFile(filepath.Join(wt, ".gitattributes"), []byte("* filter=fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := Preview(context.Background(), repo, "main", []string{"agent/filter"})
	if err != nil || len(rep.Candidates) != 1 || rep.Candidates[0].State != Unknown {
		t.Fatal("configured executable was treated as safe", rep, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "FILTER_EXECUTED")); !os.IsNotExist(err) {
		t.Fatal("read-only preview executed the clean filter", err)
	}
}
