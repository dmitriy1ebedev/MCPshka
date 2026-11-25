package quality

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualityRefusesCoverageWhenTestsFail(t *testing.T) {
	root := t.TempDir()
	src := "package a\n\nfunc Simple() int {\n\treturn 1\n}\n\nfunc Branchy(x int) int {\n\tif x > 0 && x < 10 {\n\t\treturn 1\n\t}\n\tfor i := 0; i < x; i++ {\n\t\tif i == 3 {\n\t\t\treturn i\n\t\t}\n\t}\n\treturn 0\n}\n"
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "a", "a.go"), []byte(src), 0o644)
	profile := filepath.Join(root, "cover.out")
	_ = os.WriteFile(profile, []byte("mode: set\nex/a/a.go:3.20,5.2 1 1\nex/a/a.go:7.26,8.21 1 0\nex/a/a.go:8.21,10.3 1 0\nex/a/a.go:11.2,16.2 3 0\n"), 0o644)
	if _, err := Report(root, "ex", profile, false, []string{"a/a.go"}); !errors.Is(err, ErrTestsFailed) {
		t.Fatalf("red run must refuse: %v", err)
	}
	funcs, err := Report(root, "ex", profile, true, []string{"a/a.go"})
	if err != nil || len(funcs) != 2 {
		t.Fatalf("%+v %v", funcs, err)
	}
	b := funcs[0]
	if b.Name != "Branchy" || b.Complexity != 5 || b.Coverage != 0 || b.CRAP != 30 {
		t.Fatalf("branchy: %+v", b)
	}
	if s := funcs[1]; s.Name != "Simple" || s.Coverage != 1 || s.CRAP != 1 {
		t.Fatalf("simple: %+v", s)
	}
	if err := Gate(funcs, 30); err != nil {
		t.Fatalf("threshold is inclusive: %v", err)
	}
	if err := Gate(funcs, 29); err == nil || !strings.Contains(err.Error(), "Branchy") {
		t.Fatalf("gate: %v", err)
	}
}
