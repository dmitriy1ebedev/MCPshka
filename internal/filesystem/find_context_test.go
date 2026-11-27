package filesystem

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSearchFileContextLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nHIT three\nfour\nHIT five\nsix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hit := func(b []byte) bool { return len(b) > 3 && string(b[:3]) == "HIT" }
	var out FindResult
	if _, err := searchFile(path, hit, 0, 10, 2, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 2 {
		t.Fatalf("matches: %+v", out.Matches)
	}
	a, b := out.Matches[0], out.Matches[1]
	if a.Line != 3 || !slices.Equal(a.Before, []string{"one", "two"}) || !slices.Equal(a.After, []string{"four", "HIT five"}) {
		t.Fatalf("first: %+v", a)
	}
	if b.Line != 5 || !slices.Equal(b.Before, []string{"HIT three", "four"}) || !slices.Equal(b.After, []string{"six"}) {
		t.Fatalf("second (end of file closes After): %+v", b)
	}
	// On the result limit the last match still gets its After, and the
	// resume line is the match line.
	out = FindResult{}
	line, err := searchFile(path, hit, 0, 1, 1, &out)
	if err != errResultLimit || line != 3 || !slices.Equal(out.Matches[0].After, []string{"four"}) {
		t.Fatalf("limit: %d %v %+v", line, err, out.Matches)
	}
	// Resuming after line 3 still sees line 3 as context.
	out = FindResult{}
	if _, err := searchFile(path, hit, 3, 10, 1, &out); err != nil || len(out.Matches) != 1 || !slices.Equal(out.Matches[0].Before, []string{"four"}) {
		t.Fatalf("resume: %v %+v", err, out.Matches)
	}
	// Without context nothing extra is kept.
	out = FindResult{}
	if _, err := searchFile(path, hit, 0, 10, 0, &out); err != nil || out.Matches[0].Before != nil || out.Matches[0].After != nil {
		t.Fatalf("no context: %v %+v", err, out.Matches)
	}
}
