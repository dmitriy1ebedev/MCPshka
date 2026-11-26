package projectlease

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNothingToLose(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "a")
	run("commit", "-qm", "1")
	if why := NothingToLose(dir); why != "" {
		t.Fatalf("clean merged tree kept: %s", why)
	}
	run("checkout", "-qb", "feature")
	_ = os.WriteFile(filepath.Join(dir, "a"), []byte("2"), 0o600)
	if why := NothingToLose(dir); why != "worktree has uncommitted changes" {
		t.Fatalf("dirty tree: %q", why)
	}
	run("commit", "-qam", "2")
	if why := NothingToLose(dir); why != "HEAD is not in main yet" {
		t.Fatalf("unmerged branch: %q", why)
	}
	// Only where /root/repos is visible (H96, root CI): an unprivileged CI
	// user cannot tell a removed worktree from an unreadable one.
	if _, err := os.Stat("/root/repos"); err == nil {
		if why := NothingToLose("/root/repos/definitely-gone-worktree-xyz"); why != "" {
			t.Fatalf("removed repo worktree kept: %q", why)
		}
	}
	if why := NothingToLose(filepath.Join(t.TempDir(), "gone")); why == "" {
		t.Fatal("an invisible path must not count as nothing to lose")
	}
}

func TestAutoReleaseNotesEmptyStore(t *testing.T) {
	var s *Service
	if notes := s.AutoReleaseNotes(time.Now()); len(notes) != 0 {
		t.Fatalf("nil service released: %v", notes)
	}
}
