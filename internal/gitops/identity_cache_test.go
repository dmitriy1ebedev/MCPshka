package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIdentityCacheFollowsCommits(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "a")
	git("commit", "-q", "-m", "one")
	calls := 0
	read := func() (string, error) {
		calls++
		out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
		return string(out), err
	}
	first, _ := cachedIdentity(root, "HEAD", read)
	again, _ := cachedIdentity(root, "HEAD", read)
	if first != again || calls != 1 {
		t.Fatalf("unchanged HEAD re-read: calls=%d", calls)
	}
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("2"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("commit", "-q", "-am", "two")
	second, _ := cachedIdentity(root, "HEAD", read)
	if second == first || calls != 2 {
		t.Fatalf("commit not seen: calls=%d", calls)
	}
	if refSignature(filepath.Join(root, "missing")) != "" {
		t.Fatal("unknown layout must not be cached")
	}
}
