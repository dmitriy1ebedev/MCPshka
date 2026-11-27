package projectscaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteGoProject(t *testing.T) {
	dir := t.TempDir()
	files, err := Write(dir, "demo-app", "go")
	if err != nil || len(files) < 6 {
		t.Fatalf("%v %v", files, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if !strings.Contains(string(raw), "module demo-app") {
		t.Fatal(string(raw))
	}
	road, _ := os.ReadFile(filepath.Join(dir, "ROADMAP.md"))
	if !strings.Contains(string(road), "Gates: engineering, build") {
		t.Fatal("ROADMAP lacks machine gates")
	}
	if _, err := Write(dir, "demo-app", "go"); err == nil {
		t.Fatal("overwrote an existing project")
	}
	if _, err := Write(t.TempDir(), "Bad Name", "go"); err == nil {
		t.Fatal("invalid name accepted")
	}
}
