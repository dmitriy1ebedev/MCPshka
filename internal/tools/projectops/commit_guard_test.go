package projectops

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mcp217Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestMCP217SourceGuardPinsDeletionsAndRejectsReappearingFiles(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "ui")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "old.js")
	old := []byte("old reviewed contents")
	if err := os.WriteFile(file, old, 0600); err != nil {
		t.Fatal(err)
	}
	hash := mcp217Hash(old)
	if err := verifyCommitSources(root, nil, []string{"ui/old.js"}, map[string]string{"ui/old.js": hash}); err == nil {
		t.Fatal("guarded commit admitted removal of a live pathname")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := verifyCommitSources(root, []string{"ui/old.js"}, nil, map[string]string{"ui/old.js": "DELETED"}); err != nil {
		t.Fatalf("already-removed file deletion refused: %v", err)
	}
	if err := verifyCommitSources(root, nil, []string{"ui/old.js"}, map[string]string{"ui/old.js": "DELETED"}); err == nil {
		t.Fatal("unguarded delete operation admitted absent-file sentinel")
	}
	if err := os.WriteFile(file, []byte("foreign writer restored file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCommitSources(root, []string{"ui/old.js"}, nil, map[string]string{"ui/old.js": "DELETED"}); err == nil {
		t.Fatal("external writer reappeared under deletion CAS")
	}
	if err := verifyCommitSources(root, nil, []string{"ui/old.js"}, map[string]string{"ui/old.js": hash}); err == nil {
		t.Fatal("stale delete hash unexpectedly accepted foreign rewrite")
	}
	if err := verifyCommitSources(root, []string{"ui/old.js", "ui/old.js"}, nil, map[string]string{"ui/old.js": hash}); err == nil {
		t.Fatal("duplicate commit path defeated completeness check")
	}
	if err := verifyCommitSources(root, []string{"ui/old.js"}, []string{"ui/old.js"}, map[string]string{"ui/old.js": hash}); err == nil {
		t.Fatal("file simultaneously committed and removed")
	}
}

func TestMCP217SourceGuardDetectsConcurrentWriterAndPreservesChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ui.html")
	first := []byte("<h1>new UI reviewed by holder A</h1>\n")
	foreign := []byte("<h1>old UI rewritten by holder B</h1>\n")
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]string{"ui.html": mcp217Hash(first)}
	if err := verifyCommitSources(root, []string{"ui.html"}, nil, snapshot); err != nil {
		t.Fatal("reviewed holder should pass", err)
	}
	// Simulate a separate root/RDC writer overwriting holder A's source.
	if err := os.WriteFile(path, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCommitSources(root, []string{"ui.html"}, nil, snapshot); err == nil || !strings.Contains(err.Error(), "CONCURRENT_WRITER_CONFLICT") {
		t.Fatalf("foreign rewrite was not fenced: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(foreign) {
		t.Fatal("source guard overwrote foreign changes", err, string(got))
	}
	snapshot["ui.html"] = mcp217Hash(foreign)
	if err := verifyCommitSources(root, []string{"ui.html"}, nil, snapshot); err != nil {
		t.Fatal("new reviewed snapshot was not accepted", err)
	}
}

func TestMCP217SourceGuardRequiresCompleteBoundedManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ui"), 0700); err != nil {
		t.Fatal(err)
	}
	b := []byte("safe")
	for _, name := range []string{"a.txt", "ui/b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	full := map[string]string{"a.txt": mcp217Hash(b), "ui/b.txt": mcp217Hash(b)}
	if err := verifyCommitSources(root, []string{"a.txt", "ui/b.txt"}, nil, full); err != nil {
		t.Fatal("complete manifest rejected", err)
	}
	tests := []struct {
		label    string
		files    []string
		manifest map[string]string
	}{
		{"empty manifest", []string{"a.txt"}, map[string]string{}},
		{"missing file", []string{"a.txt", "ui/b.txt"}, map[string]string{"a.txt": mcp217Hash(b)}},
		{"foreign file", []string{"a.txt"}, full},
		{"invalid hash", []string{"a.txt"}, map[string]string{"a.txt": "not-a-hash"}},
		{"traversal", []string{"../private"}, map[string]string{"../private": mcp217Hash(b)}},
		{"git metadata", []string{".git/config"}, map[string]string{".git/config": mcp217Hash(b)}},
		{"missing target", []string{"missing.txt"}, map[string]string{"missing.txt": mcp217Hash(b)}},
	}
	for _, tc := range tests {
		if err := verifyCommitSources(root, tc.files, nil, tc.manifest); err == nil ||
			!strings.Contains(err.Error(), "CONCURRENT_WRITER_CONFLICT") {
			t.Fatal(tc.label, "was not fenced", err)
		}
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := verifyCommitSources(root, []string{"link"}, nil, map[string]string{"link": mcp217Hash(b)}); err == nil {
		t.Fatal("symlink escaped guarded commit")
	}
	large := filepath.Join(root, "huge.bin")
	if err := os.WriteFile(large, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(large, commitSourceMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := verifyCommitSources(root, []string{"huge.bin"}, nil, map[string]string{"huge.bin": mcp217Hash(nil)}); err == nil {
		t.Fatal("oversize guarded file allowed")
	}
	// Existing clients can still omit this new opt-in CAS field.
	if err := verifyCommitSources(root, []string{"a.txt"}, nil, nil); err != nil {
		t.Fatal("old client path rejected", err)
	}
}
