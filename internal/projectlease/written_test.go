package projectlease

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestForeignChangesNamesFilesRewrittenAfterTheHoldersWrite(t *testing.T) {
	root := t.TempDir()
	s := testService(t, root)
	held, err := s.AcquireTracked(root, "claude", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) string {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		return hex.EncodeToString(sum[:])
	}
	s.NoteWrite(filepath.Join(root, "index.html"), held.Token, put("index.html", "new shell"))
	s.NoteWrite(filepath.Join(root, "style.css"), held.Token, put("style.css", "new css"))
	s.NoteWrite(filepath.Join(root, "gone.js"), held.Token, put("gone.js", "x"))
	put("README.md", "never written through the lease")
	if c := s.ForeignChanges(root, held.Token, []string{"index.html", "style.css", "README.md"}); len(c) != 0 {
		t.Fatalf("untouched files flagged: %v", c)
	}
	put("index.html", "old shell") // the other writer
	if err := os.Remove(filepath.Join(root, "gone.js")); err != nil {
		t.Fatal(err)
	}
	c := s.ForeignChanges(root, held.Token, []string{"index.html", "style.css", "README.md", "gone.js"})
	if len(c) != 2 || c[0] != "gone.js" || c[1] != "index.html" {
		t.Fatalf("want gone.js and index.html, got %v", c)
	}
	if c := s.ForeignChanges(root, "not-the-holder", []string{"index.html"}); c != nil {
		t.Fatalf("a non-holder reads the ledger: %v", c)
	}
}
