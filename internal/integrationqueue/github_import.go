package integrationqueue

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"mcpshka/internal/cievidence"
)

const GitHubImportRepo = "/root/repos/mcpshka"
const GitHubImportPrefix = "github-main-"

func GitHubImportPins(commit string) (id, branch string) {
	return GitHubImportPrefix + commit, "github-main-import/" + commit
}

// PrepareGitHubImport stages only a fast-forward candidate ref. Main is
// updated later by the normal sealed integration worker and publication.
func PrepareGitHubImport(ctx context.Context, repo, commit string) (MergePlan, error) {
	id, branch := GitHubImportPins(commit)
	plan, err := PlanMerge(ctx, repo, "main", commit)
	if err != nil {
		return plan, err
	}
	if !plan.FastForward {
		return plan, errors.New("GitHub candidate must advance the working main by fast-forward: HOLD")
	}
	ref := "refs/heads/" + branch
	if err := establishImportRef(ctx, repo, ref, commit, id); err != nil {
		return plan, err
	}
	return plan, nil
}

func establishImportRef(ctx context.Context, repo, ref, commit, id string) error {
	if got, exists, err := integrationReceipt(ctx, repo, ref); err != nil {
		return err
	} else if exists {
		if got != commit {
			return errors.New("GitHub import candidate ref does not match its pinned commit: HOLD")
		}
		return nil
	}
	_, err := readGit(ctx, repo, "update-ref", "--no-deref", "--create-reflog", "-m", id, ref, commit, strings.Repeat("0", 40))
	return err
}

func IsGitHubImport(id string) bool { return strings.HasPrefix(id, GitHubImportPrefix) }

// VerifyGitHubImportApproval rechecks provenance inside the root approval
// unit, independently of the daemon that fetched and staged the candidate.
func VerifyGitHubImportApproval(ctx context.Context, request Request) error {
	if !IsGitHubImport(request.ID) {
		return nil
	}
	id, branch := GitHubImportPins(request.Commit)
	if request.Path != GitHubImportRepo || request.ID != id || request.Branch != branch || request.TargetBranch != "main" {
		return errors.New("GitHub import must use the exact pinned source and main target")
	}
	raw, err := os.ReadFile(cievidence.TokenPath)
	if err != nil || len(raw) > 512 {
		return errors.New("GitHub main verification credential unavailable")
	}
	h := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return cievidence.VerifyGitHubMain(ctx, h, cievidence.APIBase, strings.TrimSpace(string(raw)), request.Commit)
}
