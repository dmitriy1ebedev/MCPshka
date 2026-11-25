package artifactcleanup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupCASAndProtected(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "artifact")
	os.WriteFile(p, []byte("abc"), 0600)
	preview, err := Run(root, []File{{Path: "artifact"}, {Path: "../outside"}, {Path: ".git/config"}}, false, nil)
	if err != nil || preview.Files[0].Status != "ok" || preview.Files[1].Status != "protected_path" || preview.Files[2].Status != "protected_path" {
		t.Fatal(preview, err)
	}
	want := preview.Files[0].File
	os.WriteFile(p, []byte("abd"), 0600)
	out, err := Run(root, []File{want}, true, nil)
	if err != nil || out.Files[0].Status != "hash_mismatch" {
		t.Fatal(out, err)
	}
	os.WriteFile(p, []byte("abc"), 0600)
	wrong := want
	wrong.Owner++
	out, _ = Run(root, []File{wrong}, true, nil)
	if out.Files[0].Status != "owner_mismatch" {
		t.Fatal(out)
	}
	out, _ = Run(root, []File{want}, true, map[string]bool{"artifact": true})
	if out.Files[0].Status != "protected_path" {
		t.Fatal(out)
	}
	os.Symlink(root, filepath.Join(root, "link"))
	out, _ = Run(root, []File{{Path: "link/artifact"}}, true, nil)
	if out.Files[0].Status != "protected_path" {
		t.Fatal(out)
	}
	out, err = Run(root, []File{want}, true, nil)
	if err != nil || out.Files[0].Status != "removed" || out.RemovedBytes != 3 {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
