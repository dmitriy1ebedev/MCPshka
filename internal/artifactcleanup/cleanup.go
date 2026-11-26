// Package artifactcleanup removes explicitly inventoried disposable worktree files.
package artifactcleanup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	Inode  uint64 `json:"inode"`
	Owner  uint32 `json:"owner"`
}
type Result struct {
	File
	Status string `json:"status"`
}
type Report struct {
	Files        []Result `json:"files"`
	RemovedBytes int64    `json:"removed_bytes"`
}

// Run requires the caller to hold the writer lease and job-admission lock.
// tracked is read from Git's index before entering; tracked data is never removed.
func Run(root string, files []File, apply bool, tracked map[string]bool) (Report, error) {
	var out Report
	if len(files) == 0 || len(files) > 64 {
		return out, errors.New("explicit list of 1..64 artifacts required")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return out, err
	}
	defer dir.Close()
	seen := map[string]bool{}
	var budget int64
	for _, want := range files {
		row := Result{File: want, Status: "protected_path"}
		p := want.Path
		safe := filepath.IsLocal(p) && filepath.Clean(p) == p && !tracked[p] && !seen[p]
		seen[p] = true
		for _, part := range strings.Split(p, "/") {
			lower := strings.ToLower(part)
			if part == ".git" || part == "bin" || part == "node_modules" || part == ".venv" || strings.HasPrefix(lower, "agent") || strings.HasPrefix(lower, ".env") || strings.Contains(lower, "rollback") || strings.Contains(lower, ".previous") || strings.Contains(lower, ".candidate") || lower == "roadmap.md" {
				safe = false
			}
		}
		if safe {
			parent, base, e := openParent(dir, p)
			if e != nil {
				if os.IsNotExist(e) {
					row.Status = "missing"
				}
			} else {
				leaf := want
				leaf.Path = base
				row = inspect(parent, leaf, apply, &budget)
				row.Path = p
				if apply && row.Status == "ok" {
					st, e := parent.Lstat(base)
					if e != nil || st.Mode()&os.ModeSymlink != 0 || st.Sys().(*syscall.Stat_t).Ino != row.Inode || st.Sys().(*syscall.Stat_t).Uid != row.Owner || st.Size() != row.Bytes {
						row.Status = "identity_mismatch"
					} else if e = parent.Remove(base); e != nil {
						row.Status = "remove_failed"
					} else {
						row.Status = "removed"
						out.RemovedBytes += row.Bytes
					}
				}
				parent.Close()
			}
		}
		out.Files = append(out.Files, row)
	}
	return out, nil
}

func inspect(dir *os.Root, want File, apply bool, budget *int64) Result {
	row := Result{File: want, Status: "unreadable"}
	f, err := dir.OpenFile(want.Path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return row
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return row
	}
	stat := st.Sys().(*syscall.Stat_t)
	if apply {
		switch {
		case want.Owner != stat.Uid:
			row.Status = "owner_mismatch"
			return row
		case want.Inode == 0 || want.Inode != stat.Ino:
			row.Status = "inode_mismatch"
			return row
		case want.Bytes != st.Size():
			row.Status = "bytes_mismatch"
			return row
		}
	}
	if st.Size() > 256<<20 || *budget+st.Size() > 1<<30 {
		row.Status = "budget_exceeded"
		return row
	}
	*budget += st.Size()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, st.Size()+1))
	if err != nil || n != st.Size() {
		return row
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if apply && want.SHA256 != sum {
		row.Status = "hash_mismatch"
		return row
	}
	row.File = File{Path: want.Path, Bytes: n, SHA256: sum, Inode: stat.Ino, Owner: stat.Uid}
	row.Status = "ok"
	return row
}

// Pin each parent directory before using it: a swapped symlink cannot redirect
// cleanup into .git, production or another protected directory.
func openParent(root *os.Root, path string) (*os.Root, string, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		st, err := current.Lstat(part)
		if err != nil {
			current.Close()
			return nil, "", err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, "", errors.New("protected parent")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			current.Close()
			return nil, "", err
		}
		bound, err := next.Stat(".")
		current.Close()
		if err != nil || !os.SameFile(st, bound) {
			next.Close()
			return nil, "", errors.New("parent changed")
		}
		current = next
	}
	return current, parts[len(parts)-1], nil
}
