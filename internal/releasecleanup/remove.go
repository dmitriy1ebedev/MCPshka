package releasecleanup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"mcpshka/internal/failure"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrBatchPending = errors.New("cleanup batch incomplete")

// Remove only within a descriptor-rooted tree, never through symlinks or a
// mount. The private parent and FENCED owner exclude concurrent writers.
func Remove(ctx context.Context, path string) error {
	started := time.Now()
	parentFD, err := syscall.Open(filepath.Dir(path), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	parent := os.NewFile(uintptr(parentFD), "cleanup-parent")
	defer parent.Close()
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", parentFD))
	if err != nil {
		return err
	}
	defer root.Close()
	st, err := root.Lstat(filepath.Base(path))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return failure.NewHold(failure.PolicyDenied, "CLEANUP_SYMLINK_FORBIDDEN", "cleanup.removeCleanupPath", "cleanup target is a symlink", nil)
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(mounts), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 && (f[4] == path || strings.HasPrefix(f[4], path+"/")) {
			return failure.NewHold(failure.PolicyDenied, "CLEANUP_MOUNT_PROTECTED", "cleanup.removeCleanupPath", "cleanup target contains a mounted filesystem", nil)
		}
	}
	paths := []string{}
	more := false
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
		if ctx.Err() != nil {
			return contextError(ctx, "cleanup.context", started)
		}
		if e != nil {
			return e
		}
		if len(paths) >= 4096 {
			more = true
			return fs.SkipAll
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		raw, ok := i.Sys().(*syscall.Stat_t)
		if !ok || raw.Dev != st.Sys().(*syscall.Stat_t).Dev || (!i.IsDir() && !i.Mode().IsRegular()) {
			return failure.NewHold(failure.PolicyDenied, "CLEANUP_TREE_UNSAFE", "cleanup.removeCleanupPath", "cleanup tree contains another device or special file", nil)
		}
		rel, e := filepath.Rel(filepath.Dir(path), p)
		if e != nil {
			return e
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return err
	}
	fresh, err := root.Lstat(filepath.Base(path))
	if err != nil {
		return err
	}
	if !os.SameFile(st, fresh) {
		return failure.NewHold(failure.FencedUnknown, "CLEANUP_TARGET_CHANGED", "cleanup.removeCleanupPath", "cleanup target inode changed", nil)
	}
	// Each validated batch is removed before the next run scans the remainder.
	removed := 0
	for i := len(paths) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return contextError(ctx, "cleanup.remove", started)
		}
		if err := root.Remove(paths[i]); err != nil && !os.IsNotExist(err) {
			if more && errors.Is(err, syscall.ENOTEMPTY) {
				continue
			}
			return err
		}
		removed++
	}
	if more {
		return fmt.Errorf("%w: pruned %d entries; continue next run", ErrBatchPending, removed)
	}
	return nil
}

func contextError(ctx context.Context, site string, started time.Time) error {
	var budget time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		budget = max(time.Duration(0), deadline.Sub(started))
	}
	return failure.Context(ctx, site, budget, ctx.Err())
}
