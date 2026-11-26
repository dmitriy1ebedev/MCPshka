package gitops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// WorktreeRoot holds the work branches' checkouts. It is owned by the
// service user, so a worktree is created and removed without root and no
// root-owned file ever lands in a repository's .git (MCP-161).
var WorktreeRoot = "/root/repos/wt"

var branchName = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,79}$`)

type WorktreeInput struct {
	Op     string `json:"op" jsonschema:"create, remove or list"`
	Path   string `json:"path" jsonschema:"repository root under /root/repos"`
	Branch string `json:"branch,omitempty" jsonschema:"work branch, e.g. feat/0.11.1; create makes it from base together with its checkout"`
	Base   string `json:"base,omitempty" jsonschema:"create: start point, default main"`
}

type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head"`
}

type WorktreeResult struct {
	Op        string     `json:"op"`
	Worktree  *Worktree  `json:"worktree,omitempty"`
	Worktrees []Worktree `json:"worktrees,omitempty"`
}

// Worktree creates, removes or lists linked worktrees. Create is atomic: the
// branch exists only together with its checkout. Remove refuses a dirty
// checkout and keeps the branch (its commits may still ship).
func (s *Service) Worktree(in WorktreeInput) (WorktreeResult, error) {
	root, err := s.RepoRoot(in.Path)
	if err != nil {
		return WorktreeResult{}, err
	}
	git := func(args ...string) (string, error) {
		out, stderr, _, _, err := runBounded(root, 120*time.Second, 256<<10, "git", append([]string{"-c", "safe.directory=*", "-c", "core.hooksPath=/dev/null"}, args...)...)
		if err != nil {
			return "", commandError("git "+args[0], err, stderr)
		}
		return string(out), nil
	}
	res := WorktreeResult{Op: in.Op}
	switch in.Op {
	case "list":
		out, err := git("worktree", "list", "--porcelain")
		if err != nil {
			return res, err
		}
		res.Worktrees = parseWorktrees(out)
		return res, nil
	case "create", "remove":
	default:
		return res, errors.New("op is create, remove or list")
	}
	if !branchName.MatchString(in.Branch) || strings.Contains(in.Branch, "..") || strings.HasSuffix(in.Branch, ".lock") || in.Branch == "main" {
		return res, fmt.Errorf("branch %q: lower-case letters, digits, . _ / -; not main", in.Branch)
	}
	dir := worktreeDir(root, in.Branch)
	if in.Op == "remove" {
		if dirty, err := git("-C", dir, "status", "--porcelain"); err != nil {
			return res, err
		} else if strings.TrimSpace(dirty) != "" {
			return res, fmt.Errorf("%s has uncommitted changes; commit them with project_commit or discard them first", dir)
		}
		if _, err = git("worktree", "remove", dir); err != nil {
			return res, err
		}
		_, err = git("worktree", "prune")
		res.Worktree = &Worktree{Path: dir, Branch: in.Branch}
		return res, err
	}
	if info, err := os.Stat(WorktreeRoot); err != nil || !info.IsDir() {
		return res, fmt.Errorf("worktree root %s is missing; create it owned by the service user", WorktreeRoot)
	}
	base := in.Base
	if base == "" {
		base = "main"
	}
	if _, err = git("rev-parse", "--verify", "--quiet", "refs/heads/"+in.Branch); err == nil {
		return res, fmt.Errorf("branch %s already exists; list its worktree or pick a new name", in.Branch)
	}
	// One git command creates the branch and its checkout; a failed add
	// drops the branch it may have made.
	if _, err = git("worktree", "add", "-b", in.Branch, dir, base); err != nil {
		_, _ = git("branch", "-D", in.Branch)
		return res, err
	}
	head, err := git("-C", dir, "rev-parse", "HEAD")
	res.Worktree = &Worktree{Path: dir, Branch: in.Branch, Head: strings.TrimSpace(head)}
	return res, err
}

// WorktreePath is where project_worktree puts the branch of repo.
func (s *Service) WorktreePath(repo, branch string) string {
	if root, err := s.RepoRoot(repo); err == nil {
		repo = root
	}
	return worktreeDir(repo, branch)
}

func worktreeDir(root, branch string) string {
	return filepath.Join(WorktreeRoot, filepath.Base(root)+"-"+strings.ReplaceAll(branch, "/", "-"))
}

func parseWorktrees(out string) []Worktree {
	list := []Worktree{}
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		w := Worktree{}
		for _, line := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(line, " ")
			switch k {
			case "worktree":
				w.Path = v
			case "HEAD":
				w.Head = v
			case "branch":
				w.Branch = strings.TrimPrefix(v, "refs/heads/")
			}
		}
		if w.Path != "" {
			list = append(list, w)
		}
	}
	return list
}

// Compact packs loose objects once enough have piled up; nothing else did
// after automated merges (35 MB of loose objects by 11.11, MCP-170).
func (s *Service) Compact(repo string) error {
	root, err := s.RepoRoot(repo)
	if err != nil {
		return err
	}
	_, stderr, _, _, err := runBounded(root, 5*time.Minute, 64<<10, "git", "-c", "safe.directory=*", "-c", "gc.auto=1500", "gc", "--auto", "--quiet")
	if err != nil {
		return commandError("git gc", err, stderr)
	}
	return nil
}
