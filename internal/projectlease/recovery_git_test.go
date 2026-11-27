package projectlease

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRecoveryGitRejectsExecutableFiltersAndGitlinks(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
	}
	git("init", "-q")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
	if err := rejectRecoveryGitExecutables(context.Background(), root); err != nil {
		t.Fatal("clean fixture rejected", err)
	}
	sentinel := filepath.Join(root, "sentinel")
	git("config", "filter.fixture.clean", "touch "+sentinel)
	os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("* filter=fixture\n"), 0600)
	os.WriteFile(filepath.Join(root, "dirty"), []byte("untrusted"), 0600)
	if err := rejectRecoveryGitExecutables(context.Background(), root); err == nil || !strings.Contains(err.Error(), "filter") {
		t.Fatal("executable filter not held", err)
	}
	if _, err := retirementGit(context.Background(), root, "status", "--porcelain", "-uall"); err == nil || !strings.Contains(err.Error(), "filter") {
		t.Fatal("root status bypassed filter guard", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("Git filter executed")
	}
	git("config", "--unset", "filter.fixture.clean")
	git("update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("a", 40)+",nested")
	if err := rejectRecoveryGitExecutables(context.Background(), root); err == nil || !strings.Contains(err.Error(), "gitlink") {
		t.Fatal("submodule config execution boundary not held", err)
	}
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "gitlink fixture")
	git("update-index", "--force-remove", "nested")
	if err := rejectRecoveryGitExecutables(context.Background(), root); err == nil || !strings.Contains(err.Error(), "gitlink") {
		t.Fatal("HEAD-only gitlink not held", err)
	}

}

// Recovery proofs scale with the repository: 3000 tracked files (MCPshka has
// 1227) must not overflow the proof budget (BUG-20261103-25).
func TestRecoveryGitProofScalesWithRepository(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
	}
	git("init", "-q")
	for i := 0; i < 3000; i++ {
		if err := os.WriteFile(filepath.Join(root, "f"+strconv.Itoa(i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "many files")
	if err := rejectRecoveryGitExecutables(context.Background(), root); err != nil {
		t.Fatal("large repository rejected:", err)
	}
}
