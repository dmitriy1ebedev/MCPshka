package filesystem

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"mcpshka/internal/policy"
)

func TestAtomicAppendBuildsAFileInChunks(t *testing.T) {
	dir := t.TempDir()
	s := New(&policy.Policy{AllowedWorkdirs: []string{dir}, DeniedCommands: map[string]struct{}{}})
	path := filepath.Join(dir, "big.txt")
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	r, err := s.AtomicWrite(path, b64("one\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AtomicAppend(path, b64("two\n"), ""); err == nil {
		t.Fatal("append without the current sha must fail")
	}
	if _, err := s.AtomicAppend(path, b64("two\n"), hex.EncodeToString(make([]byte, 32))); err == nil {
		t.Fatal("append over a stale sha must fail")
	}
	r, err = s.AtomicAppend(path, b64("two\n"), r.NewSHA256)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	sum := sha256.Sum256(raw)
	if string(raw) != "one\ntwo\n" || r.NewSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("%q %s", raw, r.NewSHA256)
	}
}
