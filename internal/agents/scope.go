package agents

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"mcpshka/internal/repopath"
)

// ScopeArgs are the trailing broker arguments for a task in path: the
// repository itself, or a worktree followed by the repository it belongs to.
// The broker cannot see /root/repos, so the service resolves the repository
// here and the runner checks it again before the agent starts.
func ScopeArgs(path string) ([]string, error) {
	path = filepath.Clean(path)
	if !strings.HasPrefix(path, "/root/repos/wt/") {
		return []string{path}, nil
	}
	repo := repopath.Worktree(path)
	if repo == "" {
		return nil, errors.New(path + " is not a linked worktree of a repository under /root/repos")
	}
	return []string{path, repo}, nil
}

// WorktreeRoot holds per-task worktrees (ROADMAP P26 line 1276).
var WorktreeRoot = "/root/repos/wt"

// OwnWorktree gives task id a conflict-free worktree of repo:
// <WorktreeRoot>/<repo>-agent-<id> on a new branch agent/<id> from HEAD, so one
// task is one writer and the main checkout stays free for releases. A retry of
// the same id returns the existing worktree.
func OwnWorktree(ctx context.Context, repo, id string) (string, error) {
	repo = filepath.Clean(repo)
	if strings.HasPrefix(repo, WorktreeRoot+"/") || !ValidTaskID(id) {
		return "", errors.New("own worktree needs a repository path and a task id")
	}
	path := filepath.Join(WorktreeRoot, filepath.Base(repo)+"-agent-"+id)
	if repopath.Worktree(path) == repo {
		return path, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "safe.directory=*", "-C", repo, "worktree", "add", "-q", "-b", "agent/"+id, path, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree for task %s: %v: %.300s", id, err, out)
	}
	return path, nil
}
