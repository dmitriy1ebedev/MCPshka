package runnerhub

import (
	"os"
	"path/filepath"
	"syscall"
)

// forge-ci and the HTTP controller share this registry across processes.
func (h *Hub) lockDisk() (*os.File, error) {
	if err := os.MkdirAll(h.Dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(h.Dir, "runners.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	keepOwner(f.Name(), h.Dir)
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
