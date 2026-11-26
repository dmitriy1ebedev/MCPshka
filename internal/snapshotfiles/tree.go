package snapshotfiles

import (
	"path/filepath"
	"strings"
)

// ValidTreeCommand bounds a registered test argv: a bare command found on the
// sandbox PATH or a committed relative executable, plain arguments.
func ValidTreeCommand(argv []string) bool {
	if len(argv) < 1 || len(argv) > 12 {
		return false
	}
	total := 0
	for _, a := range argv {
		total += len(a)
		if a == "" || len(a) > 4096 || strings.ContainsAny(a, "\x00\r\n") {
			return false
		}
	}
	if total > 16384 {
		return false
	}
	name := argv[0]
	if strings.HasPrefix(name, "./") {
		rel := strings.TrimPrefix(name, "./")
		return rel != "" && TreePath(rel)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '+') {
			return false
		}
	}
	return name != "." && name != ".."
}

func TreePath(name string) bool {
	if filepath.Clean(name) != name || strings.Contains(name, "\\") || filepath.IsAbs(name) || name == "." {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".git" || part == ".." {
			return false
		}
	}
	return true
}

// RecipePath selects the files used by the legacy Make test snapshot.
func RecipePath(name string) bool {
	if filepath.Clean(name) != name || strings.Contains(name, "\\") || filepath.IsAbs(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".git" || part == ".." {
			return false
		}
	}
	return name == "go.mod" || name == "go.sum" || name == "Makefile" || strings.HasPrefix(name, "cmd/") || strings.HasPrefix(name, "internal/") || strings.HasPrefix(name, "schemas/")
}
