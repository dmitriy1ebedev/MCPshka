package gitops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/trust"
)

type CommitResult struct {
	Trust  trust.Label `json:"trust"`
	Root   string      `json:"root"`
	Commit string      `json:"commit"`
	Files  []string    `json:"files"`
	Stat   string      `json:"stat"`
}

// integratedRoot is the MCPshka checkout whose main moves only through
// integration_ship: a direct commit there put main ahead of the protected
// store and held the next integration (BUG-20261104-36). Other projects keep
// committing on main.
var integratedRoot = "/root/repos/mcpshka"

// Commit records exactly files (repo-relative) with message, so an agent
// whose client refuses raw git can still check in a reviewed change. The
// caller holds the project lease; nothing outside files is committed.
func (s *Service) Commit(path string, files []string, message string) (CommitResult, error) {
	return s.CommitChanges(path, files, nil, message)
}

// CommitChanges also removes the delete paths from the working tree and
// commits their removal, so a client without raw git or rm can delete a file
// (14.11: README removal failed with «pathspec did not match»). A listed
// file already gone from the working tree is committed as a deletion.
func (s *Service) CommitChanges(path string, files, deletes []string, message string) (CommitResult, error) {
	return s.commitChanges(path, files, deletes, message, nil)
}

// CommitVerified checks the index prepared by Git for the requested commit.
func (s *Service) CommitVerified(path string, files []string, message string, expected map[string]string) (CommitResult, error) {
	if expected == nil {
		return CommitResult{}, errors.New("expected source hashes are required")
	}
	return s.commitChanges(path, files, nil, message, expected)
}

func (s *Service) commitChanges(path string, files, deletes []string, message string, expected map[string]string) (CommitResult, error) {
	root, err := s.RepoRoot(path)
	if err != nil {
		return CommitResult{}, err
	}
	msg := strings.TrimSpace(message)
	files = append(append([]string{}, files...), deletes...)
	if len(files) == 0 || len(files) > 200 || msg == "" || len(msg) > 4000 {
		return CommitResult{}, errors.New("commit needs 1..200 files and a 1..4000 byte message")
	}
	for _, f := range files {
		clean := filepath.Clean(f)
		if f == "" || filepath.IsAbs(f) || clean != f || strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, ".git/") || clean == ".git" {
			return CommitResult{}, fmt.Errorf("file %q must be a clean path inside the repository", f)
		}
	}
	git := func(args ...string) (string, error) {
		out, stderr, _, _, err := runBounded(root, 60*time.Second, 64<<10, "git", append([]string{"--literal-pathspecs", "-c", "safe.directory=" + root, "-c", "user.name=MCPshka", "-c", "user.email=mcpshka@localhost", "-c", "core.hooksPath=/dev/null"}, args...)...)
		if err != nil {
			return "", commandError("git "+args[0], err, stderr)
		}
		return string(out), nil
	}
	if root == integratedRoot {
		if branch, _ := git("rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(branch) == "main" {
			return CommitResult{}, errors.New("MCPshka main moves only through integration_ship: commit on a work branch (worktree), then ship it")
		}
	}
	hooks := "/dev/null"
	if expected != nil {
		hooks, err = verifiedCommitHooks(files, expected)
		if err != nil {
			return CommitResult{}, err
		}
		defer os.RemoveAll(hooks)
	}
	for _, f := range deletes {
		info, err := os.Lstat(filepath.Join(root, f))
		if err == nil && info.IsDir() {
			return CommitResult{}, fmt.Errorf("delete %q is a directory: list its files", f)
		}
		if err == nil {
			if err = os.Remove(filepath.Join(root, f)); err != nil {
				return CommitResult{}, err
			}
		}
	}
	changed, err := git(append([]string{"status", "--porcelain", "--"}, files...)...)
	if err != nil {
		return CommitResult{}, err
	}
	if strings.TrimSpace(changed) == "" {
		return CommitResult{}, errors.New("none of the files has changes")
	}
	// --only commits just these paths even if other paths are staged.
	var present, gone []string
	for _, f := range files {
		if _, err := os.Lstat(filepath.Join(root, f)); err == nil {
			present = append(present, f)
		} else {
			gone = append(gone, f)
		}
	}
	if len(present) > 0 {
		flags := []string{"add"}
		if expected != nil {
			flags = append(flags, "--intent-to-add")
		}
		if _, err = git(append(append(flags, "--"), present...)...); err != nil {
			return CommitResult{}, err
		}
	}
	if expected == nil && len(gone) > 0 {
		if _, err = git(append([]string{"rm", "-q", "--cached", "--ignore-unmatch", "--"}, gone...)...); err != nil {
			return CommitResult{}, err
		}
	}
	if _, err = git(append([]string{"-c", "core.hooksPath=" + hooks, "commit", "-q", "--only", "-m", msg, "--"}, files...)...); err != nil {
		return CommitResult{}, err
	}
	head, _ := git("rev-parse", "HEAD")
	stat, _ := git("show", "--stat", "--format=", "HEAD")
	return CommitResult{Trust: trust.Untrusted, Root: root, Commit: strings.TrimSpace(head), Files: files, Stat: strings.TrimSpace(stat)}, nil
}
