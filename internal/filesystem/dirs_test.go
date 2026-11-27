package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

// fs_write creating .gitverse/workflows/ci.yml in a project (08.11).
func TestFSWriteMissingFolders(t *testing.T) {
	root := t.TempDir()
	anchor, missing := ExistingAnchor(filepath.Join(root, ".gitverse", "workflows", "ci.yml"))
	if anchor != root || len(missing) != 2 || missing[0] != filepath.Join(root, ".gitverse") {
		t.Fatalf("anchor %s missing %v", anchor, missing)
	}
	if err := MakeMissing(missing); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(missing[1]); err != nil || !fi.IsDir() {
		t.Fatal("folders not created")
	}
	RemoveMissing(missing)
	if _, err := os.Stat(missing[0]); !os.IsNotExist(err) {
		t.Fatal("empty folders left after a failed write")
	}
	deep := root
	for i := 0; i < 12; i++ {
		deep = filepath.Join(deep, "d")
	}
	if _, m := ExistingAnchor(filepath.Join(deep, "f")); MakeMissing(m) == nil {
		t.Fatal("unbounded folder creation")
	}
}
