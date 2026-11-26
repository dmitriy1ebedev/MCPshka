package filesystem

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ROADMAP P11: the cursor walks every match exactly once, in a stable order.
func TestSearchCursorVisitsEveryMatchOnce(t *testing.T) {
	s, root := testService(t)
	write(t, filepath.Join(root, "a.txt"), "needle 1\nplain\nneedle 2\nneedle 3\n")
	write(t, filepath.Join(root, "a", "b.txt"), "needle 4\n")
	write(t, filepath.Join(root, "z.txt"), "needle 5\n")
	var got []string
	cursor := ""
	for range 10 {
		out, err := s.Search(FindQuery{Path: root, Pattern: "needle", MaxResults: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Matches {
			got = append(got, m.Text)
		}
		if cursor = out.Cursor; cursor == "" {
			if out.Status != "completed" || out.Truncated {
				t.Fatalf("last page: %+v", out)
			}
			break
		}
		if out.Status != "max_results" || !out.Truncated || out.Reason == "" {
			t.Fatalf("page: %+v", out)
		}
	}
	// WalkDir order: folder a, then a.txt, then z.txt.
	want := []string{"needle 4", "needle 1", "needle 2", "needle 3", "needle 5"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// ROADMAP P11: one unreadable folder does not cancel matches found elsewhere.
func TestSearchDeniedFolderIsPartialNotError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	s, root := testService(t)
	write(t, filepath.Join(root, "ok.txt"), "needle\n")
	locked := filepath.Join(root, ".agents")
	write(t, filepath.Join(locked, "x.txt"), "needle\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	out, err := s.Search(FindQuery{Path: root, Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "partial" || len(out.Matches) != 1 || out.Skipped.Denied != 1 || len(out.DeniedExamples) != 1 || out.DeniedExamples[0] != locked {
		t.Fatalf("denied folder: %+v", out)
	}
}

func TestSearchRegexIgnoreBinaryAndName(t *testing.T) {
	s, root := testService(t)
	write(t, filepath.Join(root, "a.go"), "func Alpha() {}\n")
	write(t, filepath.Join(root, "a_test.go"), "func AlphaTest() {}\n")
	write(t, filepath.Join(root, "gen", "b.go"), "func Beta() {}\n")
	write(t, filepath.Join(root, "blob.bin"), "func\x00Alpha")
	out, err := s.Search(FindQuery{Path: root, Pattern: `func [AB]\w+\(`, Regex: true, Ignore: []string{"*_test.go", "gen"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 1 || out.Matches[0].Text != "func Alpha() {}" || out.Skipped.Binary != 1 || out.Status != "completed" {
		t.Fatalf("regex/ignore/binary: %+v", out)
	}
	if _, err := s.Search(FindQuery{Path: root, Pattern: "(", Regex: true}); err == nil {
		t.Fatal("bad regex accepted")
	}
	if _, err := s.Search(FindQuery{Path: root, Pattern: "x", Cursor: "junk"}); err == nil {
		t.Fatal("bad cursor accepted")
	}
	names, err := s.Search(FindQuery{Path: root, Pattern: "gen", Mode: "name"})
	if err != nil || len(names.Matches) != 1 || names.Matches[0].Path != filepath.Join(root, "gen") {
		t.Fatalf("name mode: %+v %v", names, err)
	}
}

func TestSearchTimeBudgetReturnsCursor(t *testing.T) {
	s, root := testService(t)
	write(t, filepath.Join(root, "a.txt"), "needle\n")
	out, err := s.Search(FindQuery{Path: root, Pattern: "needle", Timeout: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "timed_out" || out.Cursor == "" || !out.Truncated {
		t.Fatalf("time budget: %+v", out)
	}
	rest, err := s.Search(FindQuery{Path: root, Pattern: "needle", Cursor: out.Cursor})
	if err != nil || len(rest.Matches) != 1 || rest.Status != "completed" {
		t.Fatalf("resume: %+v %v", rest, err)
	}
}

// ROADMAP P11: batch read by expected hash returns text only for changed files.
func TestReadManySkipsUnchangedAndIsolatesErrors(t *testing.T) {
	s, root := testService(t)
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	write(t, a, "alpha")
	write(t, b, "beta")
	first, err := s.ReadMany([]Known{{Path: a}, {Path: b}, {Path: filepath.Join(root, "missing")}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Text != "alpha" || first[0].SHA256 == "" || first[1].Text != "beta" || first[2].Error == "" {
		t.Fatalf("first read: %+v", first)
	}
	write(t, b, "beta2")
	again, err := s.ReadMany([]Known{{Path: a, SHA256: first[0].SHA256}, {Path: b, SHA256: first[1].SHA256}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !again[0].Unchanged || again[0].Text != "" || again[1].Unchanged || again[1].Text != "beta2" {
		t.Fatalf("second read: %+v", again)
	}
	if _, err := s.ReadMany(nil, 0); err == nil {
		t.Fatal("empty batch accepted")
	}
}
