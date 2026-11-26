package integrationqueue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ProcessLock serializes integration writers across processes on one host.
// It is advisory and must be paired with repository-scoped authorization.
type ProcessLock struct{ file *os.File }

func AcquireProcessLock(path string) (*ProcessLock, error) {
	if path == "" {
		return nil, errors.New("empty lock path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("unsafe lock directory")
	}
	// O_NOFOLLOW prevents symlink substitution at the lock file.
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("unsafe lock file")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("integration writer busy: %w", err)
	}
	return &ProcessLock{file: f}, nil
}
func (l *ProcessLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
