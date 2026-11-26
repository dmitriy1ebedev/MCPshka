// Package gitunlock removes a stale .git/index.lock as an MCP tool (moved
// out of mcpserver in 0.18.0, S5.LEAN).
package gitunlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/gitops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/fstools"
)

// A git process that dies mid-commit leaves .git/index.lock and every later
// commit fails; agents then stop (night run DIAG, BUG-20261022-36). Removing
// a lock is safe only when it is old and no git process works in the repo.

const gitLockMinAge = 30 * time.Minute

type GitUnlockInput struct {
	Path           string `json:"path" jsonschema:"Repository or worktree path."`
	LeaseToken     string `json:"lease_token,omitempty" jsonschema:"Your current lease token if you hold the project's lease."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

type GitUnlockResult struct {
	Lock    string `json:"lock"`
	Removed bool   `json:"removed"`
	AgeMin  int    `json:"age_minutes"`
}

// gitDir resolves the repository's own git directory (a worktree's gitdir).
func gitDir(root string) (string, error) {
	dotgit := filepath.Join(root, ".git")
	info, err := os.Lstat(dotgit)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return dotgit, nil
	}
	raw, err := os.ReadFile(dotgit)
	if err != nil {
		return "", err
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok || !filepath.IsAbs(dir) {
		return "", errors.New("unsupported .git file")
	}
	return filepath.Clean(dir), nil
}

// gitProcesses lists git processes; any whose working directory cannot be
// read (another user's) counts as possibly inside the repository.
func gitProcesses(root string) []string {
	var busy []string
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil || strings.TrimSpace(string(comm)) != "git" {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil || cwd == root || strings.HasPrefix(cwd, root+"/") {
			busy = append(busy, e.Name())
		}
	}
	return busy
}

func gitUnlock(leases *projectlease.Service, git *gitops.Service, in GitUnlockInput, now time.Time) (GitUnlockResult, error) {
	root, err := git.RepoRoot(in.Path)
	if err != nil {
		return GitUnlockResult{}, err
	}
	status, err := leases.Status(root)
	if err != nil {
		return GitUnlockResult{}, err
	}
	if status.Held {
		if in.LeaseToken == "" {
			return GitUnlockResult{}, errors.New("project lease is held: pass your lease_token, or wait for the holder: HOLD")
		}
		if _, err := leases.ValidateHolder(root, in.LeaseToken); err != nil {
			return GitUnlockResult{}, err
		}
	}
	dir, err := gitDir(root)
	if err != nil {
		return GitUnlockResult{}, err
	}
	lock := filepath.Join(dir, "index.lock")
	info, err := os.Lstat(lock)
	if errors.Is(err, os.ErrNotExist) {
		return GitUnlockResult{Lock: lock}, nil
	}
	if err != nil {
		return GitUnlockResult{}, err
	}
	age := now.Sub(info.ModTime())
	out := GitUnlockResult{Lock: lock, AgeMin: int(age.Minutes())}
	if !info.Mode().IsRegular() {
		return out, errors.New("index.lock is not a regular file: HOLD")
	}
	if age < gitLockMinAge {
		return out, fmt.Errorf("index.lock is %d min old (< %d): a git command may still be running; retry later: HOLD", out.AgeMin, int(gitLockMinAge.Minutes()))
	}
	if busy := gitProcesses(root); len(busy) > 0 {
		return out, fmt.Errorf("git processes may be using the repository (pids %s): HOLD", strings.Join(busy, ","))
	}
	if err := os.Remove(lock); err != nil {
		return out, err
	}
	out.Removed = true
	return out, nil
}

// Register installs project_git_unlock.
func Register(add func(tooldef.Def), store *idempotency.Store, manager *jobs.Manager, leases *projectlease.Service) {
	closed, nonDestructive := false, false
	git := gitops.New(manager.Policy())
	add(tooldef.New(&mcp.Tool{
		Name:        "project_git_unlock",
		Description: "Remove a stale .git/index.lock left by a git command that died (every commit fails while it exists): only when it is at least 30 minutes old, no git process works in the repository, and the project lease is free or held by you (lease_token).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in GitUnlockInput) (*mcp.CallToolResult, GitUnlockResult, error) {
		out, err := idempotency.Run(store, in.IdempotencyKey, "project_git_unlock", struct{ Path, Token string }{in.Path, fstools.HolderTokenDigest(in.LeaseToken)}, func() (GitUnlockResult, error) {
			return gitUnlock(leases, git, in, time.Now())
		})
		return nil, out, err
	}))
}
