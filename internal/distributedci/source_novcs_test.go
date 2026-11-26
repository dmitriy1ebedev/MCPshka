package distributedci

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The plan is built from a git archive, not a checkout: go list must not
// need VCS metadata there (it failed with "error obtaining VCS status").
func TestInventoryOutsideGitCheckout(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":                 "module mcpshka\n\ngo 1.21\n",
		"internal/a/a.go":        "package a\n",
		"internal/a/a_test.go":   "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
		"packaging/ci/plan.json": "{\"weights\":{}}\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A parent directory that looks like a broken VCS root must not matter.
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkgs, counts, _, err := inventory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0] != "mcpshka/internal/a" || counts["mcpshka/internal/a"] != 1 {
		t.Fatalf("pkgs=%v counts=%v", pkgs, counts)
	}
}

func TestCommandErrorNamesTool(t *testing.T) {
	_, err := command(context.Background(), t.TempDir(), "go", "list", "./does-not-exist/...")
	if err == nil || len(err.Error()) <= len("bounded source command failed: ") {
		t.Fatalf("opaque error: %v", err)
	}
}
