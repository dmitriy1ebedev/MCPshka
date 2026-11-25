package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactArgs(t *testing.T) {
	got := RedactArgs([]string{"--user", "alice", "--password", "hunter2", "token=abc", "--safe=value"})
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "hunter2") || strings.Contains(joined, "abc") {
		t.Fatalf("secret leaked: %q", joined)
	}
	if !strings.Contains(joined, "[REDACTED]") {
		t.Fatalf("redaction marker missing: %q", joined)
	}
}

func TestRotationAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := New(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{
		{Type: "first", Args: []string{"token=secret"}},
		{Type: "second"},
		{Type: "third"},
	} {
		if err := l.Log(event); err != nil {
			t.Fatal(err)
		}
	}
	segments, err := filepath.Glob(path + ".*")
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("append-only segments=%d want=2: %v", len(segments), segments)
	}
	for _, candidate := range append([]string{path}, segments...) {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("audit permissions for %s = %o", candidate, info.Mode().Perm())
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "secret") {
			t.Fatalf("audit segment contains secret: %q", data)
		}
	}
}
