package integrationqueue

import (
	"context"
	"errors"
	"time"
)

type MergePlan struct {
	TargetHead    string
	Candidate     string
	FastForward   bool
	AlreadyMerged bool
	Diverged      bool
}

// PlanMerge inspects commit ancestry without mutating any Git reference.
// TargetHead must be revalidated under an integration lock before applying.
func PlanMerge(ctx context.Context, repo, targetBranch, candidate string) (MergePlan, error) {
	if !safeTargetBranch(targetBranch) || !hexCommit.MatchString(candidate) {
		return MergePlan{}, errors.New("invalid merge request")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) { return readGit(ctx, repo, args...) }
	head, err := run("rev-parse", "--verify", "refs/heads/"+targetBranch+"^{commit}")
	if err != nil || !hexCommit.MatchString(head) {
		return MergePlan{}, errors.New("target branch unavailable")
	}
	if _, err = run("cat-file", "-e", candidate+"^{commit}"); err != nil {
		return MergePlan{}, errors.New("candidate unavailable")
	}
	plan := MergePlan{TargetHead: head, Candidate: candidate}
	merged, err := isAncestor(ctx, repo, candidate, head)
	if err != nil {
		return MergePlan{}, err
	}
	if merged {
		plan.AlreadyMerged = true
		return plan, nil
	}
	forward, err := isAncestor(ctx, repo, head, candidate)
	if err != nil {
		return MergePlan{}, err
	}
	if forward {
		plan.FastForward = true
		return plan, nil
	}
	plan.Diverged = true
	return plan, nil
}
