package filesystem

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// CanCreateIn reports whether a new entry directly in dir would pass the
// write policy (allowed roots, protected paths); nothing is created.
func (s *Service) CanCreateIn(dir string) error {
	_, err := s.policy.ResolveForWrite(filepath.Join(dir, ".mcpshka-write-probe"))
	return err
}

// missingUnderAllowed reports a path whose parent folders do not exist yet
// but whose nearest existing ancestor is an allowed workdir: fs_stat (the
// fs_write before-probe) answers "missing" instead of failing, so fs_write
// can create the folders (MCP-156).
func (s *Service) missingUnderAllowed(abs string) (Entry, bool) {
	dir, rest := filepath.Dir(abs), filepath.Base(abs)
	for i := 0; i < 16; i++ {
		_, err := os.Lstat(dir)
		if err == nil {
			real, err := s.policy.CheckWorkdir(dir)
			if err != nil {
				return Entry{}, false
			}
			return Entry{Path: filepath.Join(real, rest), Type: "missing"}, true
		}
		if !errors.Is(err, fs.ErrNotExist) || dir == filepath.Dir(dir) {
			return Entry{}, false
		}
		dir, rest = filepath.Dir(dir), filepath.Join(filepath.Base(dir), rest)
	}
	return Entry{}, false
}
