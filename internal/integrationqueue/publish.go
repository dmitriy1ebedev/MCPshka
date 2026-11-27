package integrationqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/failure"
)

// PublishFastForward moves the working checkout of the target branch to a
// candidate that root already recorded as integrated. It only fast-forwards
// from the admitted target head; a moved, dirty or diverged checkout is HOLD.
// Repeating it after success reports ALREADY_PUBLISHED without Git effects.
func PublishFastForward(ctx context.Context, repo, targetBranch, targetHead, candidate string) (string, error) {
	if !safeBranch(targetBranch) && targetBranch != "main" && targetBranch != "master" {
		return "", errors.New("invalid target branch")
	}
	if !hexCommit.MatchString(targetHead) || !hexCommit.MatchString(candidate) || targetHead == candidate {
		return "", errors.New("invalid publication identities")
	}
	if root, err := readGit(ctx, repo, "rev-parse", "--show-toplevel"); err != nil || root != repo {
		return "", errors.New("publication requires the exact checkout root: HOLD")
	}
	ref := "refs/heads/" + targetBranch
	if head, err := readGit(ctx, repo, "symbolic-ref", "-q", "HEAD"); err != nil || head != ref {
		// The candidate branch is usually still checked out after its commit.
		// A clean branch checkout is switched to the target here instead of
		// handing every release back to the owner; a detached HEAD, tracked
		// edits or a refused switch stay HOLD with nothing touched.
		if err := switchToTarget(ctx, repo, head, targetBranch); err != nil {
			return "", err
		}
	}
	head, err := directTargetHead(ctx, repo, ref)
	if err != nil {
		return "", err
	}
	if head == candidate {
		return "ALREADY_PUBLISHED", nil
	}
	if head != targetHead {
		return "", errors.New("target moved since admission: HOLD")
	}
	if ok, err := isAncestor(ctx, repo, targetHead, candidate); err != nil || !ok {
		return "", errors.New("candidate is not a fast-forward of the target: HOLD")
	}
	// Untracked local documents (ROADMAP, checkpoints) stay; tracked edits block.
	if dirty, err := readGit(ctx, repo, "status", "--porcelain=v1", "--untracked-files=no", "--ignore-submodules=all"); err != nil || dirty != "" {
		return "", checkoutHold("CHECKOUT_TRACKED_CHANGES", "target checkout has tracked changes: HOLD; commit or set them aside before publishing")
	}
	if err := untrackedCollisions(ctx, repo, targetHead, candidate); err != nil {
		return "", err
	}
	// A leftover index.lock made the first live publish fail with no reason
	// given (BUG-15). Name it; clear it only when it is clearly abandoned.
	state := "PUBLISHED"
	if cleared, err := clearAbandonedIndexLock(ctx, repo, time.Now()); err != nil {
		return "", err
	} else if cleared {
		state = "PUBLISHED_STALE_LOCK_CLEARED"
	}
	if err := checkoutWritable(ctx, repo, targetHead, candidate); err != nil {
		return "", err
	}
	if err := gitDirWritable(ctx, repo, targetBranch); err != nil {
		return "", err
	}
	if _, err := readGit(ctx, repo, "merge", "--ff-only", "--no-edit", candidate); err != nil {
		return "", errors.New("fast-forward outcome UNKNOWN; inspect checkout before retry")
	}
	if head, err := directTargetHead(ctx, repo, ref); err != nil || head != candidate {
		return "", errors.New("fast-forward outcome UNKNOWN; inspect checkout before retry")
	}
	return state, nil
}

func switchToTarget(ctx context.Context, repo, head, targetBranch string) error {
	hold := checkoutHold("CHECKOUT_NOT_ON_TARGET", "checkout is not on the integration target and cannot be switched safely: HOLD; switch "+repo+" to "+targetBranch+" after preserving its uncommitted work")
	if !strings.HasPrefix(head, "refs/heads/") {
		return hold
	}
	if dirty, err := readGit(ctx, repo, "status", "--porcelain=v1", "--untracked-files=no", "--ignore-submodules=all"); err != nil || dirty != "" {
		return hold
	}
	if _, err := readGit(ctx, repo, "switch", "--quiet", "--no-guess", targetBranch); err != nil {
		return hold
	}
	if now, err := readGit(ctx, repo, "symbolic-ref", "-q", "HEAD"); err != nil || now != "refs/heads/"+targetBranch {
		return errors.New("checkout switch outcome UNKNOWN; inspect checkout before retry")
	}
	return nil
}

// checkoutHold is a verified, typed refusal: nothing was changed and the
// reason is named, so callers never see HOLD_REASON_UNCLASSIFIED.
func checkoutHold(code, message string) error {
	e := failure.NewHold(failure.FailedVerified, code, "integrationqueue.PublishFastForward", message, nil)
	e.NoEffect = true // checked before merge: the checkout was not touched
	e.NextSafeAction = failure.Action{Tool: "git_status", Arguments: map[string]any{}}
	return e
}

// untrackedCollisions refuses before merging when untracked local files sit
// where the candidate adds tracked ones: `merge --ff-only` stops on them,
// and the failure used to surface as "fast-forward outcome UNKNOWN" with
// no reason (BUG-20261103-1, a local copy of the r20 registry). Local files
// are never removed here; the paths are named for the owner.
func untrackedCollisions(ctx context.Context, repo, from, to string) error {
	list, err := readGit(ctx, repo, "diff", "--name-only", "--no-renames", "--diff-filter=A", "-z", from, to)
	if err != nil {
		return checkoutHold("CHECKOUT_PATHS_UNVERIFIED", "added paths unverified: HOLD")
	}
	var hit []string
	for _, name := range strings.Split(list, "\x00") {
		if name == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(name))); err == nil {
			hit = append(hit, name)
		}
	}
	if len(hit) == 0 {
		return nil
	}
	shown := hit
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return checkoutHold("CHECKOUT_UNTRACKED_COLLISION", fmt.Sprintf("untracked files would be overwritten by the candidate: HOLD; %d path(s): %s; move them aside (an identical copy is safe to drop)", len(hit), strings.Join(shown, ", ")))
}

// staleIndexLockAge is far beyond any Git operation on this checkout; a
// younger lock may belong to a running command and is reported, not touched.
const staleIndexLockAge = 30 * time.Minute

func clearAbandonedIndexLock(ctx context.Context, repo string, now time.Time) (bool, error) {
	gitDir, err := readGit(ctx, repo, "rev-parse", "--absolute-git-dir")
	if err != nil || !filepath.IsAbs(gitDir) {
		return false, errors.New("checkout Git directory unverified: HOLD")
	}
	lock := filepath.Join(gitDir, "index.lock")
	info, err := os.Lstat(lock)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("checkout index lock unreadable: HOLD")
	}
	age := now.Sub(info.ModTime()).Round(time.Second)
	if age < staleIndexLockAge {
		return false, fmt.Errorf("checkout index is locked by another Git operation (%s old): HOLD; retry after it finishes", age)
	}
	if err := os.Remove(lock); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("abandoned index lock (%s old) could not be cleared: HOLD", age)
	}
	return true, nil
}

// checkoutWritable refuses a fast-forward this process cannot complete. Git
// replaces each changed file by unlink and create, so the parent directory
// (or, for a new directory, its nearest existing ancestor) must be writable.
// Root-owned source directories left by a manual root Git operation made a
// live publish stop half-way, with tracked files of both commits (BUG-54).
func checkoutWritable(ctx context.Context, repo, from, to string) error {
	list, err := readGit(ctx, repo, "diff", "--name-only", "--no-renames", "-z", from, to)
	if err != nil {
		return errors.New("changed paths unverified: HOLD")
	}
	seen := map[string]bool{}
	var blocked []string
	for _, name := range strings.Split(list, "\x00") {
		if name == "" {
			continue
		}
		dir := filepath.Dir(filepath.Join(repo, filepath.FromSlash(name)))
		for {
			if _, err := os.Lstat(dir); err == nil || dir == repo {
				break
			}
			dir = filepath.Dir(dir)
		}
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if syscall.Access(dir, 0x2|0x1) != nil {
			rel, _ := filepath.Rel(repo, dir)
			blocked = append(blocked, rel+"/")
		}
	}
	if len(blocked) > 0 {
		if len(blocked) > 5 {
			blocked = append(blocked[:5], "…")
		}
		return fmt.Errorf("checkout cannot write %s (fix ownership first); nothing changed: HOLD", strings.Join(blocked, ", "))
	}
	return nil
}

// gitDirWritable refuses a fast-forward whose Git metadata this process
// cannot update. A manual root Git operation left index, packed-refs, config
// and logs root-owned 0644: merge --ff-only stopped half-way and the receipt
// stayed pending with retries refused (BUG-20261024-101).
func gitDirWritable(ctx context.Context, repo, branch string) error {
	gitDir, err := readGit(ctx, repo, "rev-parse", "--absolute-git-dir")
	if err != nil || !filepath.IsAbs(gitDir) {
		return errors.New("checkout Git directory unverified: HOLD")
	}
	ref := filepath.FromSlash("refs/heads/" + branch)
	dirs := []string{".", filepath.Dir(ref), "logs", filepath.Join("logs", filepath.Dir(ref))}
	files := []string{"index", "packed-refs", "config", "ORIG_HEAD", ref, filepath.Join("logs", "HEAD"), filepath.Join("logs", ref)}
	var blocked []string
	for _, d := range dirs {
		path := filepath.Join(gitDir, d)
		if _, err := os.Lstat(path); err == nil && syscall.Access(path, 0x2|0x1) != nil {
			blocked = append(blocked, filepath.Join(".git", d)+"/")
		}
	}
	for _, f := range files {
		path := filepath.Join(gitDir, f)
		if _, err := os.Lstat(path); err == nil && syscall.Access(path, 0x2) != nil {
			blocked = append(blocked, filepath.Join(".git", f))
		}
	}
	if len(blocked) > 0 {
		return fmt.Errorf("checkout cannot update %s (chown to the service user first); nothing changed: HOLD", strings.Join(blocked, ", "))
	}
	return nil
}
