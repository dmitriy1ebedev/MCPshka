package executor

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeProtectedTestObject(t *testing.T, repo, kind string, payload []byte) string {
	t.Helper()
	raw := append([]byte(kind+" "+strconv.Itoa(len(payload))+"\x00"), payload...)
	sum := sha1.Sum(raw)
	oid := hex.EncodeToString(sum[:])
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	if _, err := z.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, "objects", oid[:2])
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, oid[2:]), compressed.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return oid
}

func protectedTestCommit(t *testing.T, repo string) (string, string) {
	t.Helper()
	blob := writeProtectedTestObject(t, repo, "blob", []byte("verified source\n"))
	oid, err := hex.DecodeString(blob)
	if err != nil {
		t.Fatal(err)
	}
	tree := writeProtectedTestObject(t, repo, "tree", append([]byte("100644 source.go\x00"), oid...))
	parent := writeProtectedTestObject(t, repo, "commit", []byte("tree "+tree+"\nauthor test <t@example.test> 1 +0000\ncommitter test <t@example.test> 1 +0000\n\nparent\n"))
	commit := writeProtectedTestObject(t, repo, "commit", []byte("tree "+tree+"\nparent "+parent+"\nauthor test <t@example.test> 2 +0000\ncommitter test <t@example.test> 2 +0000\n\ncandidate\n"))
	return commit, blob
}

func TestProtectedObjectsClosureAndCorruption(t *testing.T) {
	for _, kind := range []string{"complete", "missing-blob", "bad-compression", "hash-mismatch", "trailing-compressed-data"} {
		t.Run(kind, func(t *testing.T) {
			c, parent := protectedBirthFixture(t)
			born, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid()))
			if err != nil {
				t.Fatal(err)
			}
			commit, blob := protectedTestCommit(t, born.Repository.CommonDir)
			path := filepath.Join(born.Repository.CommonDir, "objects", blob[:2], blob[2:])
			switch kind {
			case "missing-blob":
				err = os.Remove(path)
			case "bad-compression":
				err = os.WriteFile(path, []byte("corrupt"), 0600)
			case "hash-mismatch":
				other := writeProtectedTestObject(t, born.Repository.CommonDir, "blob", []byte("altered source"))
				raw, e := os.ReadFile(filepath.Join(born.Repository.CommonDir, "objects", other[:2], other[2:]))
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(path, raw, 0600)
			case "trailing-compressed-data":
				f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				_, err = f.Write([]byte("trailing"))
				f.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(born.Repository.CommonDir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = validateProtectedObjects(context.Background(), root, commit)
			if kind == "complete" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrHold) {
				t.Fatalf("%s accepted: %v", kind, err)
			}
		})
	}
}

func TestProtectedObjectsRejectTypesMalformedTreesAndCancellation(t *testing.T) {
	c, parent := protectedBirthFixture(t)
	born, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(born.Repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	blob := writeProtectedTestObject(t, born.Repository.CommonDir, "blob", []byte("not a tree"))
	commit := writeProtectedTestObject(t, born.Repository.CommonDir, "commit", []byte("tree "+blob+"\n\ninvalid\n"))
	if err := validateProtectedObjects(context.Background(), root, commit); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	oid, _ := hex.DecodeString(blob)
	for _, entry := range []string{"100644 ..\x00", "100644 a/b\x00", "777 file\x00"} {
		tree := writeProtectedTestObject(t, born.Repository.CommonDir, "tree", append([]byte(entry), oid...))
		commit := writeProtectedTestObject(t, born.Repository.CommonDir, "commit", []byte("tree "+tree+"\n\ninvalid\n"))
		if err := validateProtectedObjects(context.Background(), root, commit); !errors.Is(err, ErrHold) {
			t.Fatalf("malformed tree accepted: %q %v", entry, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateProtectedObjects(ctx, root, strings.Repeat("1", 40)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
