package runnerhub

import (
	"os"
	"syscall"
)

// keepOwner gives path the owner of the registry directory. Root callers (the
// MOTD refresh, session hooks) read the status through update/save; without
// this the rename left a root-owned runners.json the service could not read,
// and every runner key was refused (23.11).
func keepOwner(path, dir string) {
	if os.Geteuid() != 0 {
		return
	}
	st, err := os.Stat(dir)
	if err != nil {
		return
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(s.Uid), int(s.Gid))
	}
}
