package executor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// verifyProtectedRepositoryStorage is a prerequisite, not admission authority.
// It does not prove that an old writer has no inherited writable descriptors.
// A caller must also hold a sealed born-protected repository registration and
// the exact integration generation before treating this as a writer fence.
func verifyProtectedRepositoryStorage(ctx context.Context, repo RepositoryIdentity) error {
	if os.Geteuid() != 0 || !filepath.IsAbs(repo.CommonDir) || filepath.Clean(repo.CommonDir) != repo.CommonDir ||
		!strings.HasPrefix(repo.CommonDir, "/root/repos/") || repo.Device == 0 || repo.Inode == 0 {
		return ErrHold
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrHold
	}
	defer func() { unix.Close(fd) }()
	var ancestry unix.Stat_t
	if unix.Fstat(fd, &ancestry) != nil || ancestry.Uid != 0 || ancestry.Mode&0022 != 0 {
		return ErrHold
	}
	for _, part := range strings.Split(strings.TrimPrefix(repo.CommonDir, "/"), "/") {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := unix.Openat(fd, part, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return ErrHold
		}
		unix.Close(fd)
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&0022 != 0 {
			return ErrHold
		}
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || uint64(st.Dev) != repo.Device || st.Ino != repo.Inode {
		return ErrHold
	}
	remaining := 100000
	return scanProtectedGitFD(ctx, fd, 0, uint64(st.Dev), &remaining, 0)
}

// The owner argument is private so unprivileged tests can exercise real kernel
// metadata. The production entry above always requires root ownership.
func scanProtectedGitTree(ctx context.Context, path string, owner uint32, limit int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if limit < 1 || limit > 100000 {
		return ErrHold
	}
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrHold
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return ErrHold
	}
	return scanProtectedGitFD(ctx, fd, owner, uint64(st.Dev), &limit, 0)
}

func scanProtectedGitFD(ctx context.Context, fd int, owner uint32, device uint64, remaining *int, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if *remaining < 1 || depth > 128 {
		return ErrHold
	}
	*remaining--
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != owner || st.Mode&0022 != 0 || uint64(st.Dev) != device {
		return ErrHold
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		if st.Nlink != 1 {
			return ErrHold
		}
		return nil
	case unix.S_IFDIR:
	default:
		return ErrHold
	}
	// Reopen the pinned directory, never its potentially replaced pathname.
	directory, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrHold
	}
	file := os.NewFile(uintptr(directory), "protected-git-directory")
	defer file.Close()
	for {
		names, err := file.Readdirnames(64)
		if err != nil && err != io.EOF {
			return ErrHold
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			child, err := unix.Openat(directory, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return ErrHold
			}
			check := scanProtectedGitFD(ctx, child, owner, device, remaining, depth+1)
			unix.Close(child)
			if check != nil {
				return check
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}
