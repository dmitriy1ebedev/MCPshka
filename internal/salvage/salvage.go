// Package salvage compares pinned Git candidates without mutating or authorizing cleanup.
package salvage

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	Integrated  = "integrated"
	Superseded  = "superseded"
	NeedsReview = "needs_review"
	// Retain the Go name; unmatched patches alone do not prove release state.
	NotReleased  = NeedsReview
	Unknown      = "unknown"
	WIP          = "wip"
	MaxBranches  = 64
	MaxCommits   = 200
	maxPaths     = 256
	maxGitOutput = 256 << 10
)

type Commit struct {
	SHA        string `json:"sha"`
	Subject    string `json:"subject"`
	Equivalent bool   `json:"equivalent_in_base"`
}
type Candidate struct {
	Branch           string   `json:"branch"`
	Head             string   `json:"head"`
	State            string   `json:"state"`
	Worktree         string   `json:"worktree,omitempty"`
	Dirty            bool     `json:"dirty,omitempty"`
	Pending          int      `json:"pending_commits"`
	Commits          []Commit `json:"commits,omitempty"`
	ContentEqual     int      `json:"content_equal_paths,omitempty"`
	ContentDifferent int      `json:"content_different_paths,omitempty"`
	NextSafe         string   `json:"next_safe_action"`
	Truncated        bool     `json:"truncated,omitempty"`
}
type Report struct {
	Repo       string         `json:"repo"`
	Base       string         `json:"base"`
	BaseHead   string         `json:"base_head"`
	Candidates []Candidate    `json:"candidates"`
	Summary    map[string]int `json:"summary"`
	Truncated  bool           `json:"truncated,omitempty"`
}

var branchName = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Git reads bounded local evidence; hooks, external diffs and lazy network fetches are disabled.
var Git = func(ctx context.Context, repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	argv := []string{"--no-lazy-fetch", "--no-optional-locks", "--no-replace-objects", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-C", repo}
	cmd := exec.CommandContext(ctx, "git", append(argv, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err = cmd.Start(); err != nil {
		return "", err
	}
	out, readErr := io.ReadAll(io.LimitReader(pipe, maxGitOutput+1))
	if readErr != nil || len(out) > maxGitOutput {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return "", errors.New("Git observation exceeded its output budget")
	}
	if err = cmd.Wait(); err != nil {
		return "", err
	}
	return string(out), nil
}

func validBranch(name string) bool {
	return branchName.MatchString(name) && !strings.HasPrefix(name, "-") && !strings.Contains(name, "..")
}
func Preview(ctx context.Context, repo, base string, branches []string) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if base == "" {
		base = "main"
	}
	if !validBranch(base) {
		return Report{}, errors.New("invalid base branch")
	}
	rep := Report{Repo: repo, Base: base, Summary: map[string]int{}}
	head, err := Git(ctx, repo, "rev-parse", "--verify", base+"^{commit}")
	if err != nil || !commitSHA.MatchString(strings.TrimSpace(head)) {
		return rep, errors.New("base branch unavailable")
	}
	rep.BaseHead = strings.TrimSpace(head)
	if len(branches) == 0 {
		out, err := Git(ctx, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
		if err != nil {
			return rep, err
		}
		branches = strings.Fields(out)
	}
	if len(branches) > MaxBranches {
		branches = branches[:MaxBranches]
		rep.Truncated = true
	}
	trees, err := worktrees(ctx, repo)
	if err != nil {
		return rep, err
	}
	for _, branch := range branches {
		if branch == base {
			continue
		}
		c := Candidate{Branch: branch, State: Unknown, NextSafe: "observation incomplete; preserve branch and WIP, reconcile before any integration or cleanup"}
		if validBranch(branch) {
			observed, err := inspect(ctx, repo, rep.BaseHead, branch, trees[branch])
			if err == nil {
				c = observed
			} else {
				c.Head, c.Worktree = observed.Head, observed.Worktree
			}
		}
		if ctx.Err() != nil {
			rep.Truncated = true
		}
		rep.Summary[c.State]++
		rep.Candidates = append(rep.Candidates, c)
	}
	return rep, nil
}

func exitOne(err error) bool {
	var exited *exec.ExitError
	return errors.As(err, &exited) && exited.ExitCode() == 1
}
func inspect(ctx context.Context, repo, baseHead, branch, tree string) (Candidate, error) {
	c := Candidate{Branch: branch, Worktree: tree}
	head, err := Git(ctx, repo, "rev-parse", "--verify", branch+"^{commit}")
	if err != nil || !commitSHA.MatchString(strings.TrimSpace(head)) {
		return c, errors.New("candidate head unavailable")
	}
	c.Head = strings.TrimSpace(head)
	if tree != "" {
		// Status may invoke a clean filter; an unknown executable is never a read-only probe.
		filter, err := Git(ctx, tree, "config", "--get-regexp", `^filter\..*\.(clean|process)$`)
		if err == nil && strings.TrimSpace(filter) != "" || err != nil && !exitOne(err) {
			return c, errors.New("worktree filter observation unavailable or executable")
		}
		current, err := Git(ctx, tree, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(current) != c.Head {
			return c, errors.New("worktree moved during observation")
		}
		status, err := Git(ctx, tree, "status", "--porcelain", "--untracked-files=normal")
		if err != nil {
			return c, err
		}
		c.Dirty = strings.TrimSpace(status) != ""
	}
	if _, err := Git(ctx, repo, "merge-base", "--is-ancestor", c.Head, baseHead); err == nil {
		c.State = Integrated
	} else {
		if !exitOne(err) {
			return c, err
		}
		out, err := Git(ctx, repo, "cherry", "-v", baseHead, c.Head)
		if err != nil {
			return c, err
		}
		scanner := bufio.NewScanner(strings.NewReader(out))
		for scanner.Scan() {
			fields := strings.SplitN(scanner.Text(), " ", 3)
			if len(fields) < 2 || !commitSHA.MatchString(fields[1]) || (fields[0] != "+" && fields[0] != "-") {
				return c, errors.New("invalid patch identity observation")
			}
			if len(c.Commits) >= MaxCommits {
				c.Truncated = true
				break
			}
			commit := Commit{SHA: fields[1], Equivalent: fields[0] == "-"}
			if len(fields) == 3 {
				commit.Subject = strings.ToValidUTF8(fields[2][:min(len(fields[2]), 160)], "?")
			}
			if !commit.Equivalent {
				c.Pending++
			}
			c.Commits = append(c.Commits, commit)
		}
		if err := scanner.Err(); err != nil {
			return c, err
		}
		c.State = Superseded
		if c.Pending > 0 || c.Truncated {
			c.State = NeedsReview
		}
		if c.Pending > 0 && !c.Truncated {
			equal, different, err := contentPaths(ctx, repo, baseHead, c.Head)
			if err != nil {
				return c, err
			}
			c.ContentEqual, c.ContentDifferent = equal, different
			if equal > 0 && different == 0 {
				c.State = Superseded
			}
		}
	}
	switch {
	case c.Dirty:
		c.State = WIP
		c.NextSafe = "preserve tracked and untracked WIP; review and checkpoint with its owner before any handoff or cleanup"
	case c.State == NeedsReview:
		c.NextSafe = "compare unmatched patches and content with this pinned base; check tests and production receipts; no blind cherry-pick or old-branch integration"
	default:
		c.NextSafe = "Git equivalence only; verify jobs, leases, ignored checkpoints/WIP and rollback receipts before removing any branch or worktree"
	}
	return c, nil
}

func contentPaths(ctx context.Context, repo, base, head string) (int, int, error) {
	common, err := Git(ctx, repo, "merge-base", base, head)
	if err != nil || !commitSHA.MatchString(strings.TrimSpace(common)) {
		return 0, 0, errors.New("common ancestor unavailable")
	}
	changed, err := Git(ctx, repo, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", strings.TrimSpace(common), head, "--")
	if err != nil {
		return 0, 0, err
	}
	paths := strings.Split(strings.TrimSuffix(changed, "\x00"), "\x00")
	if changed == "" {
		return 0, 0, nil
	}
	if len(paths) > maxPaths {
		return 0, 0, errors.New("candidate content comparison exceeds path budget")
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", head, base, "--"}
	delta, err := Git(ctx, repo, append(args, paths...)...)
	if err != nil {
		return 0, 0, err
	}
	different := strings.Count(delta, "\x00")
	return len(paths) - different, different, nil
}

func worktrees(ctx context.Context, repo string) (map[string]string, error) {
	out, err := Git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	path := ""
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(line, "worktree "); ok {
			path = value
		} else if value, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			m[value] = path
		}
	}
	return m, nil
}
