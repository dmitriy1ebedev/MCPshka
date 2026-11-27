package integrationqueue

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcessLockExclusionAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "integrator.lock")
	first, err := AcquireProcessLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireProcessLock(path); err == nil {
		t.Fatal("second writer admitted")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireProcessLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
}
func TestProcessLockRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireProcessLock(path); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestProcessLockRejectsWorldWritableDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireProcessLock(filepath.Join(dir, "lock")); err == nil {
		t.Fatal("unsafe directory accepted")
	}
}

func TestProcessLockRejectsPermissiveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	if err := os.WriteFile(path, []byte("x"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireProcessLock(path); err == nil {
		t.Fatal("permissive lock file accepted")
	}
}
