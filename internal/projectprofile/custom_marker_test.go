package projectprofile

import (
	"mcpshka/internal/project"
	"os"
	"path/filepath"
	"testing"
)

func TestCustomProfileMarkerNeedsRealScopedFile(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "custom.marker")
	match := Match{Markers: []string{"custom.marker"}}
	detected := project.Result{Root: root}
	if validateMatch(match, detected) == nil {
		t.Fatal("missing marker accepted")
	}
	if err := os.WriteFile(name, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateMatch(match, detected); err != nil {
		t.Fatal("declared custom marker ignored", err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), name); err != nil {
		t.Fatal(err)
	}
	if validateMatch(match, detected) == nil {
		t.Fatal("symlink marker accepted")
	}
}
