package agents

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// AbortPreparation removes only this freshly prepared task, before dispatch.
// Git refuses a changed worktree or a branch that is no longer merged.
func AbortPreparation(ctx context.Context, repo, id string, worktree bool) error {
	return abortPreparation(ctx, repo, id, worktree, TaskDir)
}

func abortPreparation(ctx context.Context, repo, id string, worktree bool, taskDir string) error {
	if !ValidTaskID(id) {
		return errors.New("invalid task id")
	}
	if worktree {
		path := filepath.Join(WorktreeRoot, filepath.Base(filepath.Clean(repo))+"-agent-"+id)
		for _, args := range [][]string{{"worktree", "remove", path}, {"branch", "-d", "agent/" + id}} {
			cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "safe.directory=*", "-C", repo}, args...)...)
			if err := cmd.Run(); err != nil {
				return errors.New("task preparation rollback failed; inspect worktree before retry")
			}
		}
	}
	return os.Remove(filepath.Join(taskDir, id+".prompt"))
}
