// Package snapshotfiles identifies the tiny, immutable input surface for
// offline whole-module tests. It never reads the host filesystem or Git.
package snapshotfiles

import (
	"path/filepath"
	"strings"
)

// SafeModulePath rejects metadata, traversal and a source-tree supplied
// replacement for the sealed dependency cache.
func SafeModulePath(name, depsDir string) bool {
	if filepath.Clean(name) != name || filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".git" || part == ".." || part == depsDir {
			return false
		}
	}
	return true
}

// ModuleDoc allows exactly the tracked files read by the spelling gate.
func ModuleDoc(name string) bool {
	switch name {
	case "README.md", "README.ru.md", "CHANGELOG.md", "BUGLOG.md":
		return true
	}
	if !strings.HasPrefix(name, "docs/") || !strings.HasSuffix(name, ".md") {
		return false
	}
	parts := strings.Split(name, "/")
	return len(parts) == 2 || len(parts) == 3 && parts[1] == "releases"
}

// FilterModuleTree receives the NUL-delimited output of a pinned git ls-tree.
// It excludes unrelated docs before the strict source materializer validates
// blob types, path safety and file counts. A malformed entry always fails.
// ModulePath is the complete allowlist for sealed module source inputs.
func ModulePath(name, depsDir string) bool {
	return SafeModulePath(name, depsDir) &&
		(ModuleDoc(name) || name == "go.mod" || name == "go.sum" || name == "VERSION" ||
			strings.HasPrefix(name, "cmd/") || strings.HasPrefix(name, "internal/") ||
			strings.HasPrefix(name, "packaging/"))
}

// ModuleTree keeps the pinned source fetch opaque to this pure package.
func ModuleTree(read func() ([]byte, error), invalid error) ([]byte, error) {
	raw, err := read()
	if err != nil {
		return nil, err
	}
	return FilterModuleTree(raw, invalid)
}

func FilterModuleTree(raw []byte, invalid error) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for _, entry := range strings.Split(string(raw), "\x00") {
		if entry == "" {
			continue
		}
		_, name, ok := strings.Cut(entry, "\t")
		if !ok {
			return nil, invalid
		}
		if strings.HasPrefix(name, "docs/") && !ModuleDoc(name) {
			continue
		}
		out = append(out, entry...)
		out = append(out, 0)
	}
	return out, nil
}
