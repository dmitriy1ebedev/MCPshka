package gitops

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// CherryPick adds a reviewed batch only to a clean, existing release worktree.
func (s *Service) CherryPick(path string, commits []string) (string, error) {
	root, err := s.RepoRoot(path)
	if err != nil {
		return "", err
	}
	if root != path || root == integratedRoot || len(commits) == 0 || len(commits) > 20 {
		return "", errors.New("existing release worktree and 1..20 commits required")
	}
	for _, commit := range commits {
		if raw, err := hex.DecodeString(commit); err != nil || len(raw) != 20 {
			return "", errors.New("full commit SHA required")
		}
	}
	git := func(args ...string) (string, error) {
		out, stderr, _, _, err := runBounded(root, 120*time.Second, 65536, "git", append([]string{"-c", "safe.directory=*", "-c", "core.hooksPath=/dev/null", "-c", "user.name=MCPshka", "-c", "user.email=mcpshka@localhost"}, args...)...)
		if err != nil {
			return "", commandError("git "+args[0], err, stderr)
		}
		return strings.TrimSpace(string(out)), nil
	}
	branch, err := git("branch", "--show-current")
	if err != nil || branch == "" || branch == "main" || branch == "master" || strings.HasPrefix(branch, "fix/") {
		return "", errors.New("release work branch required")
	}
	if dirty, err := git("status", "--porcelain"); err != nil || dirty != "" {
		return "", errors.New("release worktree must be clean")
	}
	if _, err := git(append([]string{"cherry-pick", "--no-edit"}, commits...)...); err != nil {
		return "", err
	}
	return git("rev-parse", "HEAD")
}
