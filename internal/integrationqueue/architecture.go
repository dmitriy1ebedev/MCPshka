package integrationqueue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/archlint"
)

const architectureBatchLimit = 32

// ArchitecturePreflight checks the candidate and all current-base integration
// intents. Those intents pin the revisions admitted for integration; local branch
// names alone cannot distinguish ready work from abandoned drafts.
func (s *Runtime) ArchitecturePreflight(ctx context.Context, repo, branch, candidate string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if !safeTargetBranch(branch) || !hexCommit.MatchString(candidate) {
		return errors.New("invalid architecture preflight identity")
	}
	root, key, err := s.queue(ctx, repo)
	if err != nil {
		return err
	}
	head, err := directTargetHead(ctx, root, "refs/heads/"+branch)
	if err != nil {
		return err
	}
	record, _, err := s.read(ctx, key)
	if err != nil {
		return err
	}
	commits := []string{candidate}
	for _, item := range record.Items {
		req := item.Request
		if req.TargetBranch == branch && req.TargetHead == head &&
			(item.State == Pending || item.State == Integrating) && !slices.Contains(commits, req.Commit) {
			commits = append(commits, req.Commit)
		}
	}
	if len(commits) > architectureBatchLimit {
		return errors.New("too many admitted candidates for one architecture preflight")
	}
	if err := checkMergedArchitecture(ctx, root, head, commits); err != nil {
		return err
	}
	if live, err := directTargetHead(ctx, root, "refs/heads/"+branch); err != nil || live != head {
		return errors.New("target changed during architecture preflight; retry with the current target")
	}
	return nil
}

func checkMergedArchitecture(ctx context.Context, repo, head string, commits []string) error {
	objects, err := readGit(ctx, repo, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil || !filepath.IsAbs(objects) {
		return errors.New("repository object directory unavailable")
	}
	// The service may have a small tmpfs at /tmp; the sealed test worker
	// instead provides a disk-backed TMPDIR and cannot write to /var/tmp.
	enoughSpace := func(dir string) bool {
		var disk syscall.Statfs_t
		return syscall.Statfs(dir, &disk) == nil && disk.Bavail*uint64(disk.Bsize) >= max(uint64(3<<30), disk.Blocks*uint64(disk.Bsize)/10)+(64<<20)
	}
	scratch := os.TempDir()
	if !enoughSpace(scratch) {
		scratch = "/var/tmp"
		if !enoughSpace(scratch) {
			return errors.New("architecture preflight scratch reserve unavailable")
		}
	}
	dir, err := os.MkdirTemp(scratch, "mcpshka-architecture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	baseEnv := []string{
		"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_LAZY_FETCH=1",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + objects,
		"GIT_AUTHOR_NAME=MCPshka", "GIT_COMMITTER_NAME=MCPshka",
		"GIT_AUTHOR_EMAIL=preflight@localhost", "GIT_COMMITTER_EMAIL=preflight@localhost",
	}
	run := func(input string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/git", args...)
		cmd.Dir, cmd.Env = dir, baseEnv
		cmd.Stdin = strings.NewReader(input)
		var output architectureOutput
		cmd.Stdout = &output
		if err := cmd.Run(); err != nil {
			return nil, err
		}
		return output.buf.Bytes(), nil
	}
	if _, err := run("", "init", "--bare", "--quiet", "--template="); err != nil {
		return errors.New("architecture preflight could not initialize private Git objects")
	}
	// Check the candidate itself as well as the accumulated tree. One queued
	// branch may happen to shrink a package the candidate alone grows.
	if err := checkArchitectureTree(run, commits[0]); err != nil {
		return err
	}
	for _, commit := range commits {
		tree, err := run("", "merge-tree", "--write-tree", "--no-messages", head, commit)
		treeSHA := strings.TrimSpace(string(tree))
		if err != nil || !hexCommit.MatchString(treeSHA) {
			return fmt.Errorf("architecture preflight could not merge candidate %.12s", commit)
		}
		merged, err := run("", "commit-tree", treeSHA, "-p", head, "-p", commit, "-m", "architecture preflight")
		head = strings.TrimSpace(string(merged))
		if err != nil || !hexCommit.MatchString(head) {
			return errors.New("architecture preflight could not pin the merged tree")
		}
	}
	return checkArchitectureTree(run, head)
}

const architectureReadLimit = 32 << 20

type architectureOutput struct{ buf bytes.Buffer }

func (b *architectureOutput) Write(p []byte) (int, error) {
	if len(p) > architectureReadLimit-b.buf.Len() {
		return 0, errors.New("architecture preflight output exceeds budget")
	}
	return b.buf.Write(p)
}

func checkArchitectureTree(run func(string, ...string) ([]byte, error), tree string) error {
	raw, err := run("", "ls-tree", "-r", "-z", "-l", tree, "--", "internal", "cmd")
	if err != nil {
		return errors.New("architecture preflight could not read the Git tree")
	}
	type source struct {
		name, oid string
		size      int
	}
	var files []source
	var ids strings.Builder
	var total int
	for _, entry := range strings.Split(string(raw), "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		if !ok || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 4 || fields[1] != "blob" || !hexCommit.MatchString(fields[2]) ||
			(fields[0] != "100644" && fields[0] != "100755") {
			return fmt.Errorf("architecture preflight requires regular Go files: %q", name)
		}
		size, err := strconv.Atoi(fields[3])
		if err != nil || size < 0 || size > architectureReadLimit-total-64 {
			return errors.New("architecture preflight source exceeds budget")
		}
		total += size + 64
		files = append(files, source{name, fields[2], size})
		ids.WriteString(fields[2] + "\n")
	}
	raw, err = run(ids.String(), "cat-file", "--batch")
	if err != nil {
		return errors.New("architecture preflight could not read source blobs")
	}
	counts := make(map[string]int, len(files))
	for _, f := range files {
		header, rest, found := bytes.Cut(raw, []byte("\n"))
		if !found || string(header) != fmt.Sprintf("%s blob %d", f.oid, f.size) || len(rest) <= f.size || rest[f.size] != '\n' {
			return errors.New("architecture preflight blob identity mismatch")
		}
		counts[f.name] = bytes.Count(rest[:f.size], []byte("\n"))
		raw = rest[f.size+1:]
	}
	if len(raw) != 0 {
		return errors.New("architecture preflight received unexpected source data")
	}
	if violations := archlint.Growth(counts); len(violations) != 0 {
		return fmt.Errorf("archlint on Git tree %.12s: %s", tree, strings.Join(violations[:min(3, len(violations))], "; "))
	}
	return nil
}
