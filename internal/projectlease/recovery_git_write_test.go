package projectlease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverySnapshotRejectsGitWriteAliases(t *testing.T) {
	common := filepath.Join(t.TempDir(), ".git")
	for _, rel := range []string{"objects/aa", "refs/heads", "logs/refs/heads"} {
		if err := os.MkdirAll(filepath.Join(common, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	id := strings.Repeat("a", 64)
	if err := recoveryGitWriteScope(common, id); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	for _, rel := range []string{"objects/aa", "refs/heads/mcpshka-recovery", "logs/refs/heads/mcpshka-recovery"} {
		name := filepath.Join(common, rel)
		if info, err := os.Stat(name); err == nil && info.IsDir() {
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
		if err := recoveryGitWriteScope(common, id); err == nil {
			t.Fatal("Git write alias accepted", rel)
		}
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(name, 0700); err != nil {
			t.Fatal(err)
		}
	}
}
