package filesystem

import (
	"errors"
	"golang.org/x/sys/unix"
	"mcpshka/internal/audit"
	"os"
	"path/filepath"
)

// Move uses the kernel's no-replace rename so a concurrent creator is safe.
func (s *Service) Move(source, destination string, overwrite bool) (Entry, error) {
	src, err := s.policy.ResolveForWrite(source)
	if err != nil {
		return Entry{}, err
	}
	dst, err := s.policy.ResolveForWrite(destination)
	if err != nil {
		return Entry{}, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return Entry{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Entry{}, errors.New("symlink source refused")
	}
	for _, root := range s.policy.AllowedWorkdirs {
		real, e := filepath.EvalSymlinks(root)
		if e == nil && real == src {
			return Entry{}, errors.New("cannot move allowed root")
		}
	}
	if info.IsDir() && overwrite {
		return Entry{}, errors.New("directory overwrite refused")
	}
	if old, e := os.Lstat(destination); e == nil && (!old.Mode().IsRegular() || old.Mode()&os.ModeSymlink != 0) {
		return Entry{}, errors.New("destination is not a regular file")
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if overwrite {
		flags = 0
	}
	if err = unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, flags); err != nil {
		return Entry{}, err
	}
	s.operationAudit("fs_move", map[string]any{"source": src, "destination": dst})
	return s.StatPath(dst)
}
func (s *Service) Mkdir(path string) (Entry, error) {
	path = filepath.Clean(path)
	anchor, missing := ExistingAnchor(filepath.Join(path, ".probe"))
	if err := s.CanCreateIn(anchor); err != nil {
		return Entry{}, err
	}
	if len(missing) > 0 {
		if err := MakeMissing(missing); err != nil {
			return Entry{}, err
		}
	}
	out, err := s.StatPath(path)
	if err == nil && out.Type != "directory" {
		err = errors.New("path is not a directory")
	}
	if err == nil {
		s.operationAudit("fs_mkdir", map[string]any{"path": path})
	}
	return out, err
}
func (s *Service) operationAudit(kind string, details map[string]any) {
	if s.audit != nil {
		_ = s.audit.Log(audit.Event{Type: kind, Result: "done", Details: details})
	}
}
