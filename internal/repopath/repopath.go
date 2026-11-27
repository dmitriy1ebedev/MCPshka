// Package repopath names the repository a path under /root/repos belongs to.
package repopath

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	worktreePath = regexp.MustCompile(`^/root/repos/wt/[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	repoPath     = regexp.MustCompile(`^/root/repos/[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

// IsManagedWorktreePath checks only the path form. Worktree also verifies its Git link.
func IsManagedWorktreePath(path string) bool { return worktreePath.MatchString(path) }

// Shape reports whether path has the form of a worktree and repo of an
// immediate repository. The root broker cannot see /root/repos (ProtectHome),
// so it checks only shapes; the server resolves and the runner re-checks.
func Shape(path, repo string) bool {
	return IsManagedWorktreePath(path) && repoPath.MatchString(repo) &&
		filepath.Clean(path) == path && filepath.Clean(repo) == repo
}

// Worktree is the repository /root/repos/<name> whose linked worktree
// path is ("" otherwise): its .git file points into <repo>/.git/worktrees/.
// An agent task in a worktree leases and writes that worktree only.
func Worktree(path string) string {
	if !IsManagedWorktreePath(path) {
		return ""
	}
	fi, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok || filepath.Clean(dir) != dir || filepath.Base(filepath.Dir(dir)) != "worktrees" {
		return ""
	}
	repo := filepath.Dir(filepath.Dir(filepath.Dir(dir)))
	if filepath.Base(filepath.Dir(filepath.Dir(dir))) != ".git" || !repoPath.MatchString(repo) {
		return ""
	}
	return repo
}
