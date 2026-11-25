package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/policy"
)

func TestReadWindowNeverSplitsCyrillicAndLineOffset(t *testing.T) {
	dir := t.TempDir()
	s := New(&policy.Policy{AllowedWorkdirs: []string{dir}, DeniedCommands: map[string]struct{}{}})
	path := filepath.Join(dir, "ru.txt")
	body := "первая строка\nвторая строка\nтретья\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// 5 bytes = "пе" (4) + half of "р"; from byte 1 starts inside "п".
	for _, c := range []struct {
		cursor int64
		limit  int
	}{{0, 5}, {1, 6}, {3, 7}} {
		r, err := s.Read(path, c.cursor, c.limit, false)
		if err != nil {
			t.Fatal(err)
		}
		if r.Encoding != "utf-8" || !strings.Contains(body, r.Text) || r.Text == "" {
			t.Fatalf("cursor %d limit %d: %s %q", c.cursor, c.limit, r.Encoding, r.Text)
		}
		if got := body[r.Cursor:r.NextCursor]; got != r.Text {
			t.Fatalf("cursors do not frame the text: %q vs %q", got, r.Text)
		}
	}
	off, err := s.LineOffset(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Read(path, off, 64, false)
	if err != nil || !strings.HasPrefix(r.Text, "вторая") {
		t.Fatalf("line 2: %q %v", r.Text, err)
	}
	if _, err := s.LineOffset(path, 9); err == nil {
		t.Fatal("a line past the end must fail")
	}
	bin := filepath.Join(dir, "bin")
	if err := os.WriteFile(bin, []byte{0xff, 0xfe, 0x00, 0x80}, 0o600); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Read(bin, 0, 4, false); r.Encoding != "base64" {
		t.Fatalf("binary must stay base64: %+v", r)
	}
}
