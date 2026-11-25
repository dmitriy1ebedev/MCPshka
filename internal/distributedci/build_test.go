package distributedci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectChecksStayOnAssignedShard(t *testing.T) {
	dir := t.TempDir()
	if e := os.Mkdir(filepath.Join(dir, ".ci"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, ".ci/distributed.json"), []byte(`{"checks":[{"shard":1,"argv":["/usr/bin/false"]}]}`), 0644); e != nil {
		t.Fatal(e)
	}
	if e := projectChecks(context.Background(), dir, 0); e != nil {
		t.Fatal(e)
	}
	if e := projectChecks(context.Background(), dir, 1); e == nil {
		t.Fatal("code failure hidden")
	}
}
func TestArtifactPathAndDigestBoundary(t *testing.T) {
	a := Artifact{Path: "out/distributed/diag", SHA256: strings.Repeat("a", 64), Bytes: 42}
	if !ValidArtifact(a) {
		t.Fatal(a)
	}
	for _, path := range []string{"../diag", "out/distributed/../private", "out/distributed/a/diag", "/out/distributed/diag", "out/distributed/%2fprivate", "out/distributed/file?query"} {
		a.Path = path
		if ValidArtifact(a) {
			t.Fatal(path)
		}
	}
}
func TestInventorySupportsExternalModule(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module gitverse.ru/owner/diag\n\ngo 1.21\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package diag\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte("package diag\nimport \"testing\"\nfunc TestOne(t *testing.T){}\n"), 0644); e != nil {
		t.Fatal(e)
	}
	packages, counts, _, e := inventory(context.Background(), dir)
	if e != nil || len(packages) != 1 || counts["gitverse.ru/owner/diag"] != 1 {
		t.Fatal(packages, counts, e)
	}
}
