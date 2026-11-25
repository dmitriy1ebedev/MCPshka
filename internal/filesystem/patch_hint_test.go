package filesystem

import (
	"strings"
	"testing"
)

func TestPatchMissNamesTheNearestText(t *testing.T) {
	file := "package x\n\nfunc a() {\n\treturn 1\n}\n\nfunc b() {\n\treturn 2\n}\n"
	err := patchMiss([]byte(file), "func b() {\n    return 2\n}", 0, 1)
	if err == nil || !strings.Contains(err.Error(), "closest text at line 7") || !strings.Contains(err.Error(), "whitespace/indentation only") || !strings.Contains(err.Error(), "first difference at line 8") {
		t.Fatalf("indentation miss: %v", err)
	}
	err = patchMiss([]byte(file), "func b() {\n\treturn 3\n}", 0, 1)
	if err == nil || !strings.Contains(err.Error(), "differs in text") || !strings.Contains(err.Error(), "\"\\treturn 2\"") {
		t.Fatalf("text miss: %v", err)
	}
	if err := patchMiss([]byte(file), "nothing like this\nat all", 0, 1); err == nil || !strings.Contains(err.Error(), "no similar text") {
		t.Fatalf("unrelated: %v", err)
	}
	if err := patchMiss([]byte(file), "\treturn", 2, 1); err == nil || !strings.Contains(err.Error(), "lines 4, 8") {
		t.Fatalf("ambiguous: %v", err)
	}
	crlf := strings.ReplaceAll(file, "\n", "\r\n")
	if err := patchMiss([]byte(crlf), "func a() {\n\treturn 1\n}", 0, 1); err == nil || !strings.Contains(err.Error(), "CRLF") {
		t.Fatalf("crlf: %v", err)
	}
}
