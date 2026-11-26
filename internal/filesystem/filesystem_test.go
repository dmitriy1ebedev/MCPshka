package filesystem

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/policy"
)

func testService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	return New(p), root
}

func TestReadCursorTailAndHash(t *testing.T) {
	s, root := testService(t)
	path := filepath.Join(root, "a.txt")
	content := "0123456789"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := s.Read(path, 2, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "2345" || out.Cursor != 2 || out.NextCursor != 6 || !out.Truncated {
		t.Fatalf("unexpected cursor read: %+v", out)
	}
	sum := sha256.Sum256([]byte(content))
	if out.SHA256 != hex.EncodeToString(sum[:]) || !out.HashAvailable {
		t.Fatalf("unexpected hash: %+v", out)
	}
	tail, err := s.Read(path, 0, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Text != "789" || !tail.HasEarlier || tail.Truncated {
		t.Fatalf("unexpected tail read: %+v", tail)
	}
}

func TestListBoundedAndDoesNotFollowSymlink(t *testing.T) {
	s, root := testService(t)
	if err := os.Mkdir(filepath.Join(root, "d"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"1.txt", "2.txt", "3.txt"} {
		if err := os.WriteFile(filepath.Join(root, "d", name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	out, err := s.List(root, 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 3 || !out.Truncated {
		t.Fatalf("list bounds failed: %+v", out)
	}
	for _, e := range out.Entries {
		if strings.HasPrefix(e.Path, "/etc/") {
			t.Fatalf("followed escaping symlink: %+v", e)
		}
	}
}

func TestFindContentAndLimit(t *testing.T) {
	s, root := testService(t)
	for i, body := range []string{"needle one\n", "needle two\n", "needle three\n"} {
		name := filepath.Join(root, string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.Find(root, "needle", "content", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 2 || !out.Truncated {
		t.Fatalf("find bounds failed: %+v", out)
	}
	if out.Matches[0].Line != 1 {
		t.Fatalf("unexpected line: %+v", out.Matches[0])
	}
}

func TestPatchRequiresHashAndAtomicReplacement(t *testing.T) {
	s, root := testService(t)
	path := filepath.Join(root, "cfg.txt")
	original := []byte("alpha beta alpha\n")
	if err := os.WriteFile(path, original, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(original)
	expected := hex.EncodeToString(sum[:])

	if _, err := s.Patch(path, "alpha", "omega", "bad", 2); err == nil {
		t.Fatal("expected sha mismatch")
	}
	out, err := s.Patch(path, "alpha", "omega", expected, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out.Replacements != 2 || out.OldSHA256 != expected || out.NewSHA256 == expected {
		t.Fatalf("unexpected patch result: %+v", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "omega beta omega\n" {
		t.Fatalf("patched content = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode changed: %o", info.Mode().Perm())
	}
}

func TestOutsideRootRejected(t *testing.T) {
	s, root := testService(t)
	link := filepath.Join(root, "outside")
	if err := os.Symlink("/etc/passwd", link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(link, 0, 100, false); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

func TestAtomicWriteCreateCompareAndRefuseSymlink(t *testing.T) {
	s, root := testService(t)
	target := filepath.Join(root, "new.bin")
	check, err := s.StatPath(target)
	if err != nil || check.Type != "missing" {
		t.Fatalf("missing stat: %+v %v", check, err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 3})
	first, err := s.AtomicWrite(target, encoded, "")
	if err != nil || !first.Created || first.Bytes != 4 {
		t.Fatalf("create: %+v %v", first, err)
	}
	second, err := s.AtomicWrite(target, base64.StdEncoding.EncodeToString([]byte("updated")), first.NewSHA256)
	if err != nil || second.Created || second.OldSHA256 != first.NewSHA256 {
		t.Fatalf("CAS: %+v %v", second, err)
	}
	if _, err = s.AtomicWrite(target, encoded, first.NewSHA256); err == nil {
		t.Fatal("stale SHA accepted")
	}
	if _, err = s.AtomicWrite(target, encoded, ""); err == nil {
		t.Fatal("overwrote existing without SHA")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "updated" {
		t.Fatalf("contents %q %v", got, err)
	}
	link := filepath.Join(root, "link")
	if err = os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AtomicWrite(link, encoded, second.NewSHA256); err == nil {
		t.Fatal("symlink target accepted")
	}
	metadata, err := s.StatPath(link)
	if err != nil || metadata.Type != "symlink" {
		t.Fatalf("symlink stat: %+v %v", metadata, err)
	}
}
func TestAtomicWriteRejectsEscapedTarget(t *testing.T) {
	s, root := testService(t)
	dir := filepath.Join(root, "outside")
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AtomicWrite(filepath.Join(dir, "bad"), base64.StdEncoding.EncodeToString([]byte("secret")), ""); err == nil {
		t.Fatal("escaped allowed root")
	}
}
