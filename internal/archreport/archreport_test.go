package archreport

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArchReportGraphFanAndCycles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module ex\n\ngo 1.22\n")
	write(t, root, "a/a.go", "package a\nimport (\"fmt\"; \"ex/b\")\nvar _ = fmt.Sprint\nvar _ = b.X\n")
	write(t, root, "a/a_test.go", "package a\nimport \"ex/c\"\nfunc TestA(t *T) {}\nfunc TestB(t *T) {}\nvar _ = c.Y\n")
	write(t, root, "b/b.go", "package b\nimport \"ex/c\"\nvar X = c.Y\n")
	write(t, root, "c/c.go", "package c\nvar Y = 1\n")
	write(t, root, "x/x.go", "package x\nimport \"ex/y\"\nvar _ = y.V\n")
	write(t, root, "y/y.go", "package y\nimport \"ex/x\"\nvar V = 1\nvar _ = x.Z\n")
	write(t, root, "vendor/v/v.go", "package v\n")
	r, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Package{}
	for _, p := range r.Packages {
		by[p.Path] = p
	}
	if _, ok := by["ex/vendor/v"]; ok || len(by) != 5 {
		t.Fatalf("packages: %v", by)
	}
	if a := by["ex/a"]; a.FanOut != 1 || a.Tests != 2 || a.Files != 2 {
		t.Fatalf("a: %+v (test imports must not count)", a)
	}
	if c := by["ex/c"]; c.FanIn != 1 {
		t.Fatalf("c fan-in: %+v", c)
	}
	if len(r.Cycles) != 1 || r.Cycles[0][0] != "ex/x" || r.Cycles[0][1] != "ex/y" {
		t.Fatalf("cycles: %v", r.Cycles)
	}
}
