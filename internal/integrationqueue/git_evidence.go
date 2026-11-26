package integrationqueue

import (
	"context"
	"errors"
	"time"
)

// ObserveGitEvidence reads Git facts without mutating the target worktree.
// Callers must hold a valid integration lease when using these facts to
// transition persistent queue state.
func ObserveGitEvidence(ctx context.Context, repo, targetRef, candidate string) (RecoveryEvidence, error) {
	if !safeTargetBranch(targetRef) || !hexCommit.MatchString(candidate) {
		return RecoveryEvidence{}, errors.New("invalid Git evidence request")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	git := func(args ...string) (string, error) { return readGit(ctx, repo, args...) }
	// Symbolic ref names are validated but must also be unambiguously
	// resolved under refs/heads; remote refs are not trusted as targets.
	head, err := git("rev-parse", "--verify", "refs/heads/"+targetRef+"^{commit}")
	if err != nil || !hexCommit.MatchString(head) {
		return RecoveryEvidence{}, errors.New("target ref unavailable")
	}
	evidence := RecoveryEvidence{TargetHead: head}
	if _, err = git("cat-file", "-e", candidate+"^{commit}"); err != nil {
		return evidence, nil
	}
	evidence.CandidateReachable = true
	// Only compare commit ancestry; no checkout, merge or reset is performed.
	evidence.CandidateMerged, err = isAncestor(ctx, repo, candidate, head)
	if err != nil {
		return RecoveryEvidence{}, err
	}
	// This checks the target worktree supplied by the caller, not every
	// linked worktree. A dirty integration worktree blocks reconciliation.
	status, err := git("status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return evidence, err
	}
	evidence.CleanTarget = status == ""
	return evidence, nil
}
