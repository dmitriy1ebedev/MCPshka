package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestProtectedGitTreeRejectsUnprotectedEntries(t *testing.T) {
	for _, kind := range []string{"group-write", "other-write", "symlink", "hardlink", "fifo", "foreign-owner", "budget"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "objects")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(target, "object")
			if err := os.WriteFile(file, []byte("sealed object"), 0600); err != nil {
				t.Fatal(err)
			}
			uid := uint32(os.Geteuid())
			limit := 100
			switch kind {
			case "group-write":
				if err := os.Chmod(file, 0620); err != nil {
					t.Fatal(err)
				}
			case "other-write":
				if err := os.Chmod(target, 0702); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(file, filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file, filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				uid++
			case "budget":
				limit = 1
			}
			if err := scanProtectedGitTree(context.Background(), root, uid, limit); !errors.Is(err, ErrHold) {
				t.Fatalf("unsafe %s admitted: %v", kind, err)
			}
		})
	}
}

func TestProtectedGitTreeChecksEveryDescendantAndCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "objects", "pack")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "data"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := scanProtectedGitTree(context.Background(), root, uint32(os.Geteuid()), 100); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(path, "data"), 0660); err != nil {
		t.Fatal(err)
	}
	if err := scanProtectedGitTree(context.Background(), root, uint32(os.Geteuid()), 100); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scanProtectedGitTree(ctx, root, uint32(os.Geteuid()), 100); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestProtectedGitTreeDepthAndRootSymlink(t *testing.T) {
	root := t.TempDir()
	path := root
	for i := 0; i < 129; i++ {
		path = filepath.Join(path, "d")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := scanProtectedGitTree(context.Background(), root, uint32(os.Geteuid()), 1000); !errors.Is(err, ErrHold) {
		t.Fatalf("depth accepted: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := scanProtectedGitTree(context.Background(), alias, uint32(os.Geteuid()), 1000); !errors.Is(err, ErrHold) {
		t.Fatalf("root symlink accepted: %v", err)
	}
}

func TestProtectedGitMetadataDoesNotRevokeInheritedWriter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "object")
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if err := scanProtectedGitTree(context.Background(), root, uint32(os.Geteuid()), 100); err != nil {
		t.Fatal(err)
	}
	// A scan and chmod cannot revoke a descriptor opened by a previous writer.
	// Admission must additionally establish born-protected storage or fence it.
	if _, err := writer.Write([]byte("still writable")); err != nil {
		t.Fatalf("fixture did not retain writer: %v", err)
	}
}
