package integrationqueue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchitecturePreflightChecksAllAdmittedBranchesTogether(t *testing.T) {
	queue, req, _ := runtimeFixture(t)
	ctx := context.Background()
	root := req.Path
	pkg := filepath.Join(root, "internal", "mcpserver")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, n int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(strings.Repeat("// fixture\n", n)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitTest(t, root, "checkout", "main")
	for i := range 13 {
		write(fmt.Sprintf("baseline%d.go", i), 670)
	}
	gitTest(t, root, "add", "internal")
	gitTest(t, root, "commit", "-m", "baseline below package cap")
	req.TargetHead = gitTest(t, root, "rev-parse", "HEAD")
	branch := func(name string) string {
		t.Helper()
		gitTest(t, root, "checkout", "-b", name, req.TargetHead)
		write(name+".go", 25)
		gitTest(t, root, "add", "internal")
		gitTest(t, root, "commit", "-m", name)
		return gitTest(t, root, "rev-parse", "HEAD")
	}
	req.Branch, req.Commit = "first", branch("first")
	second := branch("second")
	if err := queue.ArchitecturePreflight(ctx, root, "main", second); err != nil {
		t.Fatalf("branch alone: %v", err)
	}
	if _, err := queue.Enqueue(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := queue.ArchitecturePreflight(ctx, root, "main", second); err == nil || !strings.Contains(err.Error(), "internal/mcpserver has 8760 lines, cap 8751") {
		t.Fatalf("combined preflight = %v", err)
	}
	if refs := gitTest(t, root, "rev-parse", "main", "HEAD"); refs != req.TargetHead+"\n"+second {
		t.Fatalf("preflight changed refs: %s", refs)
	}
	if _, err := queue.CancelAs(ctx, root, req.ID, req.Commit, req.Owner); err != nil {
		t.Fatal(err)
	}
	if err := queue.ArchitecturePreflight(ctx, root, "main", second); err != nil {
		t.Fatalf("cancelled branch was counted: %v", err)
	}
}
