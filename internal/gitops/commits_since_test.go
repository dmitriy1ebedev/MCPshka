package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// 0.11.7: a roadmap that was once committed and then left the index (Lane)
// is untracked — counted from its modification time and never committed.
func TestCommitsSinceTreatsFormerlyTrackedFileAsUntracked(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	file := filepath.Join(root, "ROADMAP.md")
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name+time.Now().String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("ROADMAP.md")
	git("add", "ROADMAP.md")
	git("commit", "-q", "-m", "roadmap")
	write("a")
	git("add", "a")
	git("commit", "-q", "-m", "a")
	s := &Service{}
	if n, tracked := s.CommitsSince(root, file); n != 1 || !tracked {
		t.Fatalf("tracked roadmap: %d %v", n, tracked)
	}
	git("rm", "-q", "--cached", "ROADMAP.md")
	git("commit", "-q", "-m", "untrack")
	if err := os.Chtimes(file, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, tracked := s.CommitsSince(root, file); n != 0 || tracked {
		t.Fatalf("formerly tracked roadmap must count from its mtime, untracked: %d %v", n, tracked)
	}
}
