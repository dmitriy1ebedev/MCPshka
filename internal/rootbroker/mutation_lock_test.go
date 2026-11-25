package rootbroker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMutationLockFencesConcurrentConfigWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.lock")
	first, err := openMutationLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := openMutationLock(path); err == nil {
		second.Close()
		t.Fatal("concurrent config writer admitted")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := openMutationLock(path)
	if err != nil {
		t.Fatal("completed writer left a lock", err)
	}
	next.Close()
}

func TestProtectedReceiptsPersistAndRejectExclusiveReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := writeRootOnly(path, map[string]string{"state": "authorized"}, true); err != nil {
		t.Fatal(err)
	}
	if err := writeRootOnly(path, map[string]string{"state": "replaced"}, true); err == nil {
		t.Fatal("exclusive receipt replay overwrote authorization")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := saveJSONState(f, map[string]string{"state": "applied"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]string
	if err := json.Unmarshal(content, &receipt); err != nil || receipt["state"] != "applied" {
		t.Fatalf("durable state lost: %s %v", content, err)
	}
}

func TestMutationLockRejectsLinksAndUnsafePermissions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if f, err := openMutationLock(link); err == nil {
		f.Close()
		t.Fatal("symlink lock accepted")
	}
	if err := os.Link(file, filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if f, err := openMutationLock(file); err == nil {
		f.Close()
		t.Fatal("aliased lock accepted")
	}
	os.Remove(filepath.Join(dir, "alias"))
	os.Chmod(file, 0666)
	if f, err := openMutationLock(file); err == nil {
		f.Close()
		t.Fatal("publicly writable lock accepted")
	}
}
