package gitops

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcpshka/internal/policy"
	"mcpshka/internal/trust"
)

const (
	DefaultOutputLimit = 32 * 1024
	MaxOutputLimit     = 128 * 1024
)

type Service struct {
	policy *policy.Policy
}

func New(p *policy.Policy) *Service {
	if p == nil {
		p = policy.Default()
	}
	return &Service{policy: p}
}

type StatusResult struct {
	Trust    trust.Label `json:"trust"`
	Root     string      "json:\"root\""
	Branch   string      "json:\"branch,omitempty\""
	Upstream string      "json:\"upstream,omitempty\""
	Ahead    int         "json:\"ahead,omitempty\""
	Behind   int         "json:\"behind,omitempty\""
	Entries  []string    "json:\"entries\""
	Dirty    bool        "json:\"dirty\""
	// TrackedDirty ignores untracked files ("??"): local ROADMAP, registries
	// and checkpoints that publication also leaves alone.
	TrackedDirty bool "json:\"tracked_dirty\""
	Untracked    int  "json:\"untracked,omitempty\""
	Truncated    bool "json:\"truncated\""
}

type DiffResult struct {
	Trust      trust.Label `json:"trust"`
	Root       string      "json:\"root\""
	Cached     bool        "json:\"cached\""
	Text       string      "json:\"text\""
	TotalBytes int64       "json:\"total_bytes\""
	Truncated  bool        "json:\"truncated\""
}

func (s *Service) Status(path string, limit int) (StatusResult, error) {
	root, err := s.RepoRoot(path)
	if err != nil {
		return StatusResult{}, err
	}
	if limit <= 0 {
		limit = DefaultOutputLimit
	}
	if limit > MaxOutputLimit {
		limit = MaxOutputLimit
	}
	stdout, stderr, total, truncated, err := runBounded(
		root,
		5*time.Second,
		limit,
		"git", "-c", "color.ui=false", "-C", root,
		"status", "--porcelain=v1", "-b", "--untracked-files=normal",
	)
	if err != nil {
		return StatusResult{}, commandError("git status", err, stderr)
	}
	result := parseStatus(root, string(stdout))
	result.Truncated = truncated || total > int64(limit)
	return result, nil
}

func (s *Service) Diff(path string, cached bool, contextLines, limit int) (DiffResult, error) {
	root, err := s.RepoRoot(path)
	if err != nil {
		return DiffResult{}, err
	}
	if contextLines < 0 {
		contextLines = 3
	}
	if contextLines > 20 {
		contextLines = 20
	}
	if limit <= 0 {
		limit = 64 * 1024
	}
	if limit > MaxOutputLimit {
		limit = MaxOutputLimit
	}
	args := []string{
		"-c", "color.ui=false", "-C", root,
		"--no-pager", "diff", "--no-ext-diff", "--no-textconv",
		"--unified=" + strconv.Itoa(contextLines),
	}
	if cached {
		args = append(args, "--cached")
	}
	stdout, stderr, total, truncated, err := runBounded(root, 8*time.Second, limit, "git", args...)
	if err != nil {
		return DiffResult{}, commandError("git diff", err, stderr)
	}
	return DiffResult{
		Trust: trust.Untrusted, Root: root, Cached: cached, Text: string(stdout),
		TotalBytes: total, Truncated: truncated || total > int64(limit),
	}, nil
}

// CommonDir returns the Git directory shared by a checkout and its sibling
// worktrees, read from .git and commondir without running Git.
func CommonDir(root string) (string, error) {
	dotgit := filepath.Join(root, ".git")
	info, err := os.Lstat(dotgit)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return dotgit, nil
	}
	raw, err := os.ReadFile(dotgit)
	if err != nil || len(raw) > 4096 {
		return "", errors.New("unreadable .git file")
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok {
		return "", errors.New("unexpected .git file")
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(root, gitdir)
	}
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return filepath.Clean(gitdir), nil
	}
	dir := strings.TrimSpace(string(common))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(gitdir, dir)
	}
	return filepath.Clean(dir), nil
}

func (s *Service) RepoRoot(path string) (string, error) {
	real, err := s.policy.ResolveExisting(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	dir := real
	if !info.IsDir() {
		dir = filepath.Dir(real)
	}
	for {
		checked, err := s.policy.ResolveExisting(dir)
		if err != nil || checked != dir {
			break
		}
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil &&
			(info.IsDir() || info.Mode().IsRegular()) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("no git repository found for %q", path)
}

// Revision reads one bounded immutable Git identity through the same policy.
func (s *Service) Revision(path string) (string, error) {
	return s.objectIdentity(path, "HEAD")
}

// IsAncestor reports whether the full commit id is HEAD or one of its
// ancestors (a phase checkpoint survives the ROADMAP-closing commit).
func (s *Service) IsAncestor(path, commit string) (bool, error) {
	if len(commit) != 40 && len(commit) != 64 {
		return false, errors.New("invalid Git revision")
	}
	if _, err := hex.DecodeString(commit); err != nil {
		return false, errors.New("invalid Git revision")
	}
	root, err := s.RepoRoot(path)
	if err != nil {
		return false, err
	}
	_, stderr, _, _, err := runBounded(root, 3*time.Second, 128, "git", "merge-base", "--is-ancestor", commit, "HEAD")
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, commandError("git ancestry", err, stderr)
}

func (s *Service) Tree(path string) (string, error) {
	return s.objectIdentity(path, "HEAD^{tree}")
}

func (s *Service) objectIdentity(path, object string) (string, error) {
	root, err := s.RepoRoot(path)
	if err != nil {
		return "", err
	}
	return cachedIdentity(root, object, func() (string, error) {
		out, stderr, _, truncated, err := runBounded(root, 3*time.Second, 128, "git", "rev-parse", "--verify", object)
		if err != nil {
			return "", commandError("git revision", err, stderr)
		}
		value := strings.TrimSpace(string(out))
		if truncated || len(value) != 40 && len(value) != 64 {
			return "", errors.New("invalid Git revision")
		}
		if _, err := hex.DecodeString(value); err != nil {
			return "", errors.New("invalid Git revision")
		}
		return value, nil
	})
}

func parseStatus(root, text string) StatusResult {
	result := StatusResult{Trust: trust.Untrusted, Root: root}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return result
	}
	if len(lines) > 0 && strings.HasPrefix(lines[0], "## ") {
		parseBranchLine(strings.TrimPrefix(lines[0], "## "), &result)
		lines = lines[1:]
	}
	for _, line := range lines {
		if line != "" {
			result.Entries = append(result.Entries, line)
		}
	}
	result.Dirty = len(result.Entries) > 0
	for _, e := range result.Entries {
		if strings.HasPrefix(e, "?? ") {
			result.Untracked++
		} else {
			result.TrackedDirty = true
		}
	}
	return result
}

func parseBranchLine(line string, result *StatusResult) {
	mainPart := line
	if i := strings.Index(mainPart, " ["); i >= 0 {
		meta := mainPart[i+2:]
		mainPart = mainPart[:i]
		meta = strings.TrimSuffix(meta, "]")
		for _, item := range strings.Split(meta, ", ") {
			switch {
			case strings.HasPrefix(item, "ahead "):
				result.Ahead, _ = strconv.Atoi(strings.TrimPrefix(item, "ahead "))
			case strings.HasPrefix(item, "behind "):
				result.Behind, _ = strconv.Atoi(strings.TrimPrefix(item, "behind "))
			}
		}
	}
	if left, right, ok := strings.Cut(mainPart, "..."); ok {
		result.Branch = left
		result.Upstream = right
	} else {
		result.Branch = mainPart
	}
}

// ErrOwnershipUnverified is Git's refusal of a repository owned by another
// user ("dubious ownership"). MCPshka never bypasses it with safe.directory;
// the owner registers the repository instead (MCP-017).
var ErrOwnershipUnverified = errors.New("GIT_OWNERSHIP_UNVERIFIED")

func ownershipError(name string, stderr []byte) error {
	if !bytes.Contains(stderr, []byte("dubious ownership")) {
		return nil
	}
	dir := ""
	if m := dubiousDir.FindSubmatch(stderr); m != nil {
		dir = string(m[1])
	}
	owner := "unknown"
	if st, err := os.Stat(dir); dir != "" && err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			owner = strconv.FormatUint(uint64(sys.Uid), 10)
		}
	}
	return fmt.Errorf("%s: %w: repository %q is owned by uid %s, not uid %d; next_action: project_repo_adopt path=%s (no safe.directory bypass)", name, ErrOwnershipUnverified, dir, owner, os.Geteuid(), dir)
}

var dubiousDir = regexp.MustCompile(`dubious ownership in repository at '([^'\n]+)'`)

func commandError(name string, err error, stderr []byte) error {
	if oerr := ownershipError(name, stderr); oerr != nil {
		return oerr
	}
	msg := strings.TrimSpace(string(stderr))
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if msg == "" {
		return fmt.Errorf("%s: %w", name, err)
	}
	return fmt.Errorf("%s: %w: %s", name, err, msg)
}

type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	total     int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *limitedBuffer) Stats() (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total, b.truncated
}

func runBounded(workdir string, timeout time.Duration, limit int, command string, args ...string) ([]byte, []byte, int64, bool, error) {
	if limit <= 0 {
		return nil, nil, 0, false, errors.New("limit must be > 0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = workdir
	cmd.Env = policy.MinimalEnv("/var/lib/mcpshka/home")
	var stdout, stderr limitedBuffer
	stdout.limit = limit
	stderr.limit = 8 * 1024
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timeout after %s", timeout)
	}
	total, truncated := stdout.Stats()
	return stdout.Bytes(), stderr.Bytes(), total, truncated, err
}

// RecentCommits returns up to n "<short sha> <age> <subject>" lines of HEAD,
// for reports that must say what was actually committed lately.
func (s *Service) RecentCommits(path string, n int) ([]string, error) {
	root, err := s.RepoRoot(path)
	if err != nil {
		return nil, err
	}
	n = min(max(n, 1), 20)
	out, stderr, _, _, err := runBounded(root, 3*time.Second, 8192, "git", "log", "-n", fmt.Sprint(n), "--format=%h %cr %s")
	if err != nil {
		return nil, commandError("git log", err, stderr)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > 160 {
				l = l[:160]
			}
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// CommitsSince counts the HEAD commits made after file last changed: after
// the last commit touching it when tracked, else after its modification time
// (MCPshka and Lane keep ROADMAP.md untracked in the main checkout). n is -1
// when Git cannot tell.
func (s *Service) CommitsSince(root, file string) (n int, tracked bool) {
	count := func(args ...string) int {
		out, _, _, _, err := runBounded(root, 3*time.Second, 64, "git", append([]string{"rev-list", "--count"}, args...)...)
		n, aerr := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil || aerr != nil {
			return -1
		}
		return n
	}
	// Tracked now, not "was once in history": Lane's ROADMAP left the index
	// long ago and its last commit is hundreds of commits back.
	if rel, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(rel, "..") {
		if _, _, _, _, err := runBounded(root, 3*time.Second, 256, "git", "ls-files", "--error-unmatch", "--", rel); err == nil {
			last, _, _, _, err := runBounded(root, 3*time.Second, 256, "git", "log", "-1", "--format=%H", "--", rel)
			if sha := strings.TrimSpace(string(last)); err == nil && sha != "" {
				return count(sha + "..HEAD"), true
			}
			return -1, true
		}
	}
	st, err := os.Stat(file)
	if err != nil {
		return -1, false
	}
	return count(fmt.Sprintf("--since=%d", st.ModTime().Unix()), "HEAD"), false
}
