package opencore

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// CISnapshot keeps public Git objects isolated from the canonical repository.
type CISnapshot struct {
	Manifest     Manifest
	Dir, Tree    string
	gitDir       string
	scratch      string
	verifiedTree string
}

func StageCI(ctx context.Context, repo, commit string, rules []Rule) (*CISnapshot, error) {
	// The process temp dir: a sealed test sandbox has no writable /var/tmp
	// (26.11 release test failed on a hard-coded path).
	scratchRoot := os.TempDir()
	var fs syscall.Statfs_t
	if syscall.Statfs(scratchRoot, &fs) != nil || fs.Bavail*uint64(fs.Bsize) < max(uint64(3<<30), fs.Blocks*uint64(fs.Bsize)/10)+(128<<20) {
		return nil, errors.New("open core scratch disk reserve unavailable")
	}
	scratch, err := os.MkdirTemp(scratchRoot, "mcpshka-open-ci-")
	if err != nil {
		return nil, err
	}
	s := &CISnapshot{scratch: scratch, Dir: filepath.Join(scratch, "export"), gitDir: filepath.Join(scratch, "objects.git")}
	normalized := normalize(rules)
	keep := false
	defer func() {
		if !keep {
			s.Close()
		}
	}()
	if !sha40.MatchString(commit) {
		return nil, ErrBlocked
	}
	// Existing owner identifiers are sanitized by Export. A new occurrence
	// in the candidate's patch must be reviewed before it can be published.
	diff, err := gitOut(ctx, repo, "diff-tree", "--root", "--no-commit-id", "-m", "--first-parent", "--no-ext-diff", "--no-textconv", "-r", "-p", "-U0", commit)
	if err != nil {
		return nil, ErrBlocked
	}
	inHunk := false
	for _, line := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			if _, changed := apply([]byte(line[1:]), normalized); changed {
				return nil, ErrBlocked
			}
			for _, finding := range scanLine(line[1:]) {
				if finding.Kind == "secret" {
					return nil, ErrBlocked
				}
			}
		}
	}
	s.Manifest, err = Export(ctx, repo, commit, s.Dir, rules)
	if err != nil || len(s.Manifest.Blocking) != 0 {
		return nil, errors.New("HOLD: open core export audit failed")
	}
	if _, err = gitOut(ctx, scratch, "init", "--bare", "--template=", s.gitDir); err != nil {
		return nil, err
	}
	if _, err = gitOut(ctx, s.gitDir, "--work-tree="+s.Dir, "add", "--force", "--all"); err != nil {
		return nil, err
	}
	tree, err := gitOut(ctx, s.gitDir, "write-tree")
	if err != nil {
		return nil, err
	}
	s.Tree = strings.TrimSpace(string(tree))
	if err = s.verifyTree(ctx); err != nil {
		return nil, err
	}
	s.verifiedTree = s.Tree
	keep = true
	return s, nil
}

func (s *CISnapshot) Close() { _ = os.RemoveAll(s.scratch) }

func (s *CISnapshot) verifyTree(ctx context.Context) error {
	raw, err := gitOut(ctx, s.gitDir, "ls-tree", "-rz", s.Tree)
	if err != nil {
		return err
	}
	type treeEntry struct {
		path string
		mode int
		sum  [sha256.Size]byte
	}
	entries := make([]treeEntry, 0, s.Manifest.Files)
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00") {
		meta, name, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || !filepath.IsLocal(name) {
			return ErrBlocked
		}
		path := filepath.Join(s.Dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return ErrBlocked
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h := sha1.New()
		fmt.Fprintf(h, "blob %d\x00", len(data))
		h.Write(data)
		mode := 0644
		if info.Mode()&0100 != 0 {
			mode = 0755
		}
		if fields[0] != fmt.Sprintf("100%o", mode) || fields[2] != fmt.Sprintf("%x", h.Sum(nil)) {
			return ErrBlocked
		}
		entries = append(entries, treeEntry{name, mode, sha256.Sum256(data)})
	}
	if len(entries) != s.Manifest.Files {
		return ErrBlocked
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	digest := sha256.New()
	for _, entry := range entries {
		fmt.Fprintf(digest, "%o %x %s\n", entry.mode, entry.sum, entry.path)
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != s.Manifest.Digest {
		return ErrBlocked
	}
	return nil
}

// AllowInventory gives the service worker read-only access when root is
// rechecking the plan. The bare repository stays private to this process.
func (s *CISnapshot) AllowInventory() error {
	if os.Geteuid() != 0 {
		return nil
	}
	if err := os.Chmod(s.scratch, 0711); err != nil {
		return err
	}
	return filepath.WalkDir(s.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := os.Chown(path, 0, 994); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() | 0040
		if d.IsDir() {
			mode |= 0010
		}
		return os.Chmod(path, mode)
	})
}
