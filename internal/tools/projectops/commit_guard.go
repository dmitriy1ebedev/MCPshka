package projectops

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const commitSourceMaxBytes = 16 << 20

// verifyCommitSources is an opt-in source integrity gate (MCP-217). It is
// invoked WITHIN the holder's cross-process lease fence before Git stages any
// change. A client must provide a complete SHA-256 manifest for every file
// it asks to commit. External writers cannot silently replace a reviewed file
// before this check; preserving the changed file is more important than commit.
// This is not a substitute for OS-level fencing of out-of-band root writers.
func verifyCommitSources(root string, files, deleted []string, expected map[string]string) error {
	if expected == nil {
		return nil // preserve existing clients; guarded mode is opt-in
	}
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: repository cannot be pinned: %w", err)
	}
	defer sourceRoot.Close()
	if len(deleted) != 0 {
		return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: expected_files requires deletions already absent and pinned as DELETED in files")
	}
	paths := files
	unique := make(map[string]bool, len(paths))
	for _, file := range paths {
		if !filepath.IsLocal(file) || filepath.Clean(file) != file ||
			file == ".git" || strings.HasPrefix(file, ".git"+string(filepath.Separator)) {
			return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: invalid source path")
		}
		if unique[file] {
			return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: duplicate commit path %q", file)
		}
		unique[file] = true
	}
	if len(unique) != len(expected) {
		return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: expected_files must cover exactly all commit paths")
	}
	for path := range expected {
		if !unique[path] {
			return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: unexpected source path %q", path)
		}
	}

	for _, path := range paths {
		want := expected[path]
		if want == "DELETED" {
			// An absent leaf must still have a real parent directory.
			parts := strings.Split(path, string(filepath.Separator))
			current := root
			for i, part := range parts {
				current = filepath.Join(current, part)
				info, err := os.Lstat(current)
				if i == len(parts)-1 {
					if !os.IsNotExist(err) {
						return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: deleted file reappeared or cannot be inspected %q", path)
					}
				} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: unsafe deleted-file parent %q", path)
				}
			}
			continue
		}
		if len(want) != 64 {
			return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: missing or invalid SHA-256 for %q", path)
		}
		if _, err := hex.DecodeString(want); err != nil {
			return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: invalid SHA-256 for %q", path)
		}
		current := root
		parts := strings.Split(path, string(filepath.Separator))
		for i, part := range parts {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil || info.Mode()&os.ModeSymlink != 0 ||
				(i == len(parts)-1 && !info.Mode().IsRegular()) {
				return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: missing, non-regular or symlink source %q", path)
			}
		}
		f, err := sourceRoot.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: source unreadable %q", path)
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: source is not a regular file %q", path)
		}
		limited, readErr := io.ReadAll(io.LimitReader(f, commitSourceMaxBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(limited) > commitSourceMaxBytes {
			return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: source read failed or exceeded bounded limit %q", path)
		}
		actual := sha256.Sum256(limited)
		if !strings.EqualFold(hex.EncodeToString(actual[:]), want) {
			return fmt.Errorf("CONCURRENT_WRITER_CONFLICT: source changed since approved SHA-256 %q", path)
		}
	}
	return nil
}
