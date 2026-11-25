package repoadopt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The test user cannot chown to anyone else, so plans target its own uid and
// gid; the counts, pins and refusals are what is under test.
func testRoots(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	repos := filepath.Join(home, "repos")
	if err := os.Mkdir(repos, 0o755); err != nil {
		t.Fatal(err)
	}
	oldRepos, oldHome := ReposRoot, homeRoot
	ReposRoot, homeRoot = repos, home
	t.Cleanup(func() { ReposRoot, homeRoot = oldRepos, oldHome })
	return home, repos
}

func TestTargetAndSourceBoundaries(t *testing.T) {
	_, repos := testRoots(t)
	for _, bad := range []string{repos, repos + "/a/b", repos + "/../x", repos + "/.hidden", repos + "/mcpshka-controlled", repos + "/rootledger", "/etc/x"} {
		if ValidateTarget(bad) == nil {
			t.Fatal("target accepted", bad)
		}
	}
	if ValidateTarget(repos+"/free-my-tv") != nil {
		t.Fatal("immediate repository rejected")
	}
	for _, bad := range []string{homeRoot + "/.ssh", homeRoot + "/repos", homeRoot + "/mcpshka-run", homeRoot + "/a/b", "/tmp/x"} {
		if ValidateSource(bad) == nil {
			t.Fatal("source accepted", bad)
		}
	}
}

func TestCreatePinsAndRefusesARaceForTheName(t *testing.T) {
	_, repos := testRoots(t)
	uid, gid := os.Getuid(), os.Getgid()
	// MCPshka jobs inherit RestrictSUIDSGID; probe a private fixture so an
	// unrelated Apply permission error can never turn into a skipped test.
	modeProbe := t.TempDir()
	modeErr := os.Chmod(modeProbe, 0700|os.ModeSetgid)
	if modeErr != nil && !errors.Is(modeErr, os.ErrPermission) {
		t.Fatal(modeErr)
	}
	setgidAllowed := modeErr == nil
	target := filepath.Join(repos, "newproj")
	p, err := Preview(target, "", uid, gid)
	if err != nil || !p.Create || p.State != "PREVIEW" {
		t.Fatal(p, err)
	}
	applied, err := Apply(target, "", uid, gid, p.Digest)
	if setgidAllowed {
		if err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, os.ErrPermission) || applied.State == "APPLIED" {
		t.Fatal("setgid-restricted create must fail closed", applied, err)
	}
	st, err := os.Stat(target)
	if err != nil || !st.IsDir() {
		t.Fatal("created repository mode", st, err)
	}
	if (st.Mode()&os.ModeSetgid != 0) != setgidAllowed || (setgidAllowed && st.Mode().Perm() != 0o770) {
		t.Fatal("created repository mode", st.Mode())
	}
	// The same pin no longer matches: the target exists now.
	if _, err := Apply(target, "", uid, gid, p.Digest); err == nil {
		t.Fatal("stale create pin applied twice")
	}
}

func TestMoveFromHomeKeepsSymlinksUnfollowed(t *testing.T) {
	home, repos := testRoots(t)
	uid, gid := os.Getuid(), os.Getgid()
	src := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repos, "proj")
	p, err := Preview(target, src, uid, gid)
	if err != nil || p.Create || p.Entries != 4 || p.RejectedCount != 0 {
		t.Fatal(p, err)
	}
	if _, err := Apply(target, src, uid, gid, p.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "sub", "f")); err != nil {
		t.Fatal("moved tree missing", err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatal("source still present", err)
	}
	// A move never replaces an existing target.
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(target, src, uid, gid); err == nil {
		t.Fatal("move onto an existing target previewed")
	}
}

func TestDriftAndSpecialFilesHold(t *testing.T) {
	_, repos := testRoots(t)
	uid, gid := os.Getuid(), os.Getgid()
	target := filepath.Join(repos, "proj")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := Preview(target, "", uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "late"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(target, "", uid, gid, p.Digest); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal("drift after preview applied", err)
	}
	if err := syscall.Mkfifo(filepath.Join(target, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err = Preview(target, "", uid, gid)
	if err != nil || p.State != "HOLD" || p.RejectedCount != 1 || !strings.Contains(p.Rejected[0], "special") {
		t.Fatal("FIFO not held", p, err)
	}
	if _, err := Apply(target, "", uid, gid, p.Digest); err == nil {
		t.Fatal("held plan applied")
	}
}

func TestHardLinksFollowTheKernelProtection(t *testing.T) {
	_, repos := testRoots(t)
	target := filepath.Join(repos, "proj")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "a"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(target, "a"), filepath.Join(target, "b")); err != nil {
		t.Fatal(err)
	}
	p, err := Preview(target, "", os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	if HardlinksProtected() != (p.RejectedCount == 0) {
		t.Fatal("hard links vs fs.protected_hardlinks", HardlinksProtected(), p)
	}
}
