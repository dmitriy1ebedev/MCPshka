package integrationqueue

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

type fastForwardResult struct {
	TargetHead string
	Applied    bool
}

// applyFastForward is an internal effect, not an authorization endpoint. A
// coordinator must hold the repository writer fence and persist its intent
// before calling it. The common-directory lock serializes cooperating linked
// worktrees; Git's old-value comparison protects against target SHA drift.
func applyFastForward(ctx context.Context, repo, branch, expected, candidate, execution string, beforeCAS func() error) (fastForwardResult, error) {
	if !safeTargetBranch(branch) || !hexCommit.MatchString(expected) || !hexCommit.MatchString(candidate) || !requestID.MatchString(execution) {
		return fastForwardResult{}, errors.New("invalid integration effect")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	common, err := readGit(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || !filepath.IsAbs(common) {
		return fastForwardResult{}, errors.New("Git authority unavailable: HOLD")
	}
	lock, err := AcquireProcessLock(filepath.Join(common, "mcpshka-integration.lock"))
	if err != nil {
		return fastForwardResult{}, err
	}
	defer lock.Close()
	ref := "refs/heads/" + branch
	receiptRef := "refs/mcpshka/integration/" + runtimeDigest(execution)
	probe := func() (string, error) {
		return directTargetHead(ctx, repo, ref)
	}
	head, err := probe()
	if err != nil {
		return fastForwardResult{}, err
	}
	receipt, found, err := integrationReceipt(ctx, repo, receiptRef)
	if err != nil {
		return fastForwardResult{}, err
	}
	if found && (receipt != candidate || head != candidate) {
		return fastForwardResult{}, errors.New("prior integration effect or target drift: HOLD")
	}
	// Reconcile an already completed exact mutation without repeating any effect.
	if head == candidate {
		return fastForwardResult{TargetHead: head}, nil
	}
	if head != expected {
		return fastForwardResult{}, errors.New("target changed: HOLD")
	}
	check := func() error {
		worktrees, err := readGit(ctx, repo, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return errors.New("worktree inventory unavailable: HOLD")
		}
		for _, field := range strings.Split(worktrees, "\x00") {
			if field == "branch "+ref {
				return errors.New("target is checked out: HOLD")
			}
		}
		status, err := readGit(ctx, repo, "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil || status != "" {
			return errors.New("source worktree dirty or unverified: HOLD")
		}
		return nil
	}
	if err := check(); err != nil {
		return fastForwardResult{}, err
	}
	plan, err := PlanMerge(ctx, repo, branch, candidate)
	if err != nil || !plan.FastForward || plan.TargetHead != expected {
		return fastForwardResult{}, errors.New("fast-forward plan rejected: HOLD")
	}
	// A separate fsynced attempt marker must precede the target effect. Git's
	// multi-ref transaction is not a crash-atomic journal on the files backend.
	// A marker with an unchanged target remains ambiguous and is never replayed.
	if _, err := readGit(ctx, repo, "update-ref", "--no-deref", receiptRef, candidate, strings.Repeat("0", 40)); err != nil {
		return fastForwardResult{}, errors.New("attempt marker outcome unverified: UNKNOWN")
	}
	if beforeCAS != nil {
		if err := beforeCAS(); err != nil {
			return fastForwardResult{}, err
		}
	}
	if err := check(); err != nil {
		return fastForwardResult{}, err
	}
	if observed, err := probe(); err != nil || observed != expected {
		return fastForwardResult{}, errors.New("target changed before effect: HOLD")
	}
	_, updateErr := readGit(ctx, repo, "update-ref", "--no-deref", "--create-reflog", "-m", "mcpshka integration "+execution, ref, candidate, expected)
	// Even a cancelled/failed command may have committed its reference update.
	observeCtx, observeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer observeCancel()
	observed, err := directTargetHead(observeCtx, repo, ref)
	marker, exists, markerErr := integrationReceipt(observeCtx, repo, receiptRef)
	if err == nil && observed == candidate && markerErr == nil && exists && marker == candidate {
		return fastForwardResult{TargetHead: observed, Applied: true}, nil
	}
	if updateErr != nil {
		return fastForwardResult{}, updateErr
	}
	return fastForwardResult{}, errors.New("integration outcome unverified: UNKNOWN")
}
