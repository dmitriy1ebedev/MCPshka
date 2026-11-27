package projectlease

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryArchivePreservesRawBytesIndexAndSymlinks(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) string {
		t.Helper()
		out, err := recoveryGitInput(ctx, root, nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("init", "-q")
	name := " leading name.txt"
	if err := os.WriteFile(filepath.Join(root, name), []byte("committed\r\n"), 0640); err != nil {
		t.Fatal(err)
	}
	git("add", "--", name)
	tree := git("write-tree")
	commit, err := recoveryGitInput(ctx, root, bytes.NewBufferString("fixture"), "commit-tree", tree)
	if err != nil {
		t.Fatal(err)
	}
	git("update-ref", "HEAD", commit)
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	dirty := []byte("unstaged\r\nexact bytes\x00\n")
	if err := os.WriteFile(filepath.Join(root, name), dirty, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := recoveryArchive(ctx, root, &archive); err != nil {
		t.Fatal(err)
	}
	foundWork, foundIndex, foundLink := false, false, false
	tr := tar.NewReader(&archive)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		switch h.Name {
		case "worktree/" + name:
			foundWork = bytes.Equal(body, dirty)
		case "recovery/index":
			foundIndex = bytes.Equal(body, indexBefore)
		case "worktree/link":
			foundLink = h.Typeflag == tar.TypeSymlink && h.Linkname == "../outside"
		}
	}
	if !foundWork || !foundIndex || !foundLink {
		t.Fatal("recovery archive lost raw state", foundWork, foundIndex, foundLink)
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	if !bytes.Equal(indexBefore, indexAfter) {
		t.Fatal("original index changed")
	}
	got, _ := os.ReadFile(filepath.Join(root, name))
	if !bytes.Equal(got, dirty) {
		t.Fatal("original work changed")
	}
	file, err := os.CreateTemp(t.TempDir(), "snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := recoveryArchive(ctx, root, file); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	ref, err := storeRecoverySnapshot(filepath.Join(root, ".git"), id, file)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := storeRecoverySnapshot(filepath.Join(root, ".git"), id, file); err != nil || replay != ref {
		t.Fatal("snapshot replay changed reference", err)
	}
	restored, err := recoveryGitInput(ctx, root, nil, "show", ref+":worktree.tar")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := recoveryDigest(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(restored))
	if hex.EncodeToString(sum[:]) != digest {
		t.Fatal("durable snapshot does not restore exact raw bytes")
	}

}
