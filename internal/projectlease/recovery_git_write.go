package projectlease

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Root plumbing writes only loose objects and one recovery ref. Repository
// metadata is untrusted: a symlink must not redirect those writes into state.
func recoveryGitWriteScope(common, id string) error {
	if !retirementHex(id, 64) {
		return errors.New("invalid recovery reference")
	}
	paths := []string{"objects", "objects/info", "objects/pack", "refs", "refs/heads", "refs/heads/mcpshka-recovery", "logs", "logs/refs", "logs/refs/heads", "logs/refs/heads/mcpshka-recovery"}
	shards, err := os.ReadDir(filepath.Join(common, "objects"))
	if err != nil {
		return err
	}
	if len(shards) > 512 {
		return errors.New("Git object directory budget exceeded")
	}
	for _, entry := range shards {
		paths = append(paths, "objects/"+entry.Name())
	}
	for _, rel := range paths {
		name := filepath.Join(common, rel)
		info, err := os.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		real, err := filepath.EvalSymlinks(name)
		if err != nil || real != name || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("aliased Git write directory: HOLD")
		}
	}
	for _, rel := range []string{"packed-refs", "refs/heads/mcpshka-recovery/" + id, "logs/refs/heads/mcpshka-recovery/" + id} {
		info, err := os.Lstat(filepath.Join(common, rel))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("aliased Git write file: HOLD")
		}
	}
	for _, rel := range []string{"objects/info/alternates", "objects/info/http-alternates", "reftable"} {
		if _, err := os.Lstat(filepath.Join(common, rel)); !os.IsNotExist(err) {
			return errors.New("alternate Git storage is not accepted for root snapshot: HOLD")
		}
	}
	if real, err := filepath.EvalSymlinks(common); err != nil || strings.TrimSuffix(real, "/") != common {
		return errors.New("common Git authority alias")
	}
	return nil
}
