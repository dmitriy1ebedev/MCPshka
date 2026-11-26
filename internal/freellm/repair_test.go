package freellm

import (
	"strings"
	"testing"
)

func TestRepairOldFixesIndentOnlyMisses(t *testing.T) {
	file := "package x\n\nfunc f(a int) int {\n\tif a > 0 {\n\t\treturn a\n\t}\n\treturn 0\n}\n"
	// the model used four spaces and dropped the outer indent
	old := "if a > 0 {\n    return a\n}"
	repl := "if a > 1 {\n    return a - 1\n}"
	o, n, ok := repairOld(file, old, repl)
	if !ok || strings.Count(file, o) != 1 {
		t.Fatalf("not repaired: %q %v", o, ok)
	}
	got := strings.Replace(file, o, n, 1)
	if !strings.Contains(got, "\tif a > 1 {\n\t\treturn a - 1\n\t}\n") {
		t.Fatalf("re-indent: %q", got)
	}
	dup := file + "func g(a int) int {\n\tif a > 0 {\n\t\treturn a\n\t}\n\treturn 0\n}\n"
	if _, _, ok := repairOld(dup, old, repl); ok {
		t.Fatal("an ambiguous window must not be repaired")
	}
	if _, _, ok := repairOld(file, "return 0", "return 1"); ok {
		t.Fatal("a short single line is too weak an anchor")
	}
	if _, _, ok := repairOld(file, "if b > 0 {\n return b\n}", "x"); ok {
		t.Fatal("different text must not match")
	}
}
