package archreport

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// MCP-068: moving code between files keeps the surface; changing exported
// API or dropping a test changes it.
func TestSurfaceStableUnderPureMove(t *testing.T) {
	mk := func(files map[string]string) []string {
		root := t.TempDir()
		write(t, root, "go.mod", "module ex\n")
		for k, v := range files {
			write(t, root, k, v)
		}
		s, err := Surface(root)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := mk(map[string]string{
		"a/a.go":      "package a\nfunc Do(x int) error { return nil }\nfunc helper() {}\ntype T struct{}\nfunc (T) M() {}\n",
		"a/a_test.go": "package a\nimport \"testing\"\nfunc TestDo(t *testing.T) {}\n",
	})
	moved := mk(map[string]string{
		"a/do.go":     "package a\nfunc Do(x int) error { return nil }\n",
		"a/t.go":      "package a\ntype T struct{}\nfunc (T) M() {}\nfunc helper() {}\n",
		"a/a_test.go": "package a\nimport \"testing\"\nfunc TestDo(t *testing.T) {}\n",
	})
	if !slices.Equal(before, moved) {
		t.Fatalf("pure move changed the surface:\n%v\n%v", before, moved)
	}
	changed := mk(map[string]string{
		"a/a.go":      "package a\nfunc Do(x, y int) error { return nil }\ntype T struct{}\nfunc (T) M() {}\n",
		"a/a_test.go": "package a\n",
	})
	if slices.Equal(before, changed) {
		t.Fatal("signature change and dropped test must change the surface")
	}
	_ = os.Remove(filepath.Join(t.TempDir(), "x"))
}
