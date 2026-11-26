package releasecleanup

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func PermitLock(authority, id string, uid uint32, create bool) (*os.File, error) {
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(filepath.Join(authority, id+".lock"), flags, 0600)
	if !create && os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || st.Uid != uid || st.Nlink != 1 || info.Mode().Perm()&0077 != 0 {
			err = errors.New("unsafe cleanup lock")
		}
	}
	if err == nil {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func PreserveOrphanAge(path string, remove func() error) (resultErr error) {
	dir, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || time.Since(info.ModTime()) < 24*time.Hour {
		return errors.New("orphan age changed")
	}
	// Own partial removal must not make an old orphan young for another day.
	// The shared lock excludes writers; use the directory FD, never a new path.
	defer func() {
		fresh, err := dir.Stat()
		if err != nil {
			resultErr = errors.Join(resultErr, err)
			return
		}
		if fresh.ModTime() != info.ModTime() && fresh.Sys().(*syscall.Stat_t).Nlink != 0 {
			st := info.Sys().(*syscall.Stat_t)
			resultErr = errors.Join(resultErr, syscall.Futimes(int(dir.Fd()), []syscall.Timeval{syscall.NsecToTimeval(st.Atim.Nano()), syscall.NsecToTimeval(st.Mtim.Nano())}))
		}
	}()
	return remove()
}
