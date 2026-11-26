package filesystem

import (
	"errors"
	"os"
	"path/filepath"
)

// ExistingAnchor returns the deepest existing folder above path and the
// folders below it that must be created (outermost first, at most 8).
func ExistingAnchor(path string) (string, []string) {
	dir := filepath.Dir(path)
	var missing []string
	for len(missing) <= 8 {
		if _, err := os.Lstat(dir); err == nil || !errors.Is(err, os.ErrNotExist) {
			break
		}
		missing = append([]string{dir}, missing...)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dir, missing
}

func MakeMissing(dirs []string) error {
	if len(dirs) > 8 {
		return errors.New("fs_write creates at most 8 new folders")
	}
	for i, d := range dirs {
		if err := os.Mkdir(d, 0o755); err != nil {
			RemoveMissing(dirs[:i])
			return err
		}
	}
	return nil
}

func RemoveMissing(dirs []string) {
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // only if still empty
	}
}
