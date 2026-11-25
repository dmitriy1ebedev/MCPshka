package archlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoNewPython is the S5.GO ratchet: no new Python file, and a file
// ported to Go or deleted leaves the allowlist in the same change.
func TestNoNewPython(t *testing.T) {
	raw, err := os.ReadFile("python_allowlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			allowed[line] = true
		}
	}
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Dot directories hold caches (.gomodcache in sealed workspaces) and
		// VCS data, never MCPshka sources.
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".py") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		if !allowed[rel] {
			t.Errorf("%s: new Python file — MCPshka is Go-only (S5.GO)", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for f := range allowed {
		// A sealed workspace may omit whole directories (docs/history): only
		// a file missing from a directory that is present counts as gone.
		if _, err := os.Stat(filepath.Join(root, filepath.Dir(f))); err != nil {
			continue
		}
		if !seen[f] {
			t.Errorf("%s is gone: remove it from python_allowlist.txt", f)
		}
	}
}
