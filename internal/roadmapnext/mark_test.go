package roadmapnext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const markRoadmap = "# R\n\n## P1 Первая\n- [x] a\n- [ ] b\n  - [-] c\n**Приёмка** ok\n\n## P2 Вторая\n- [ ] d\n"

func TestMarkDoneFlipsOnlyPhaseItems(t *testing.T) {
	it, err := Next(markRoadmap)
	if err != nil || it.ID != "P1" {
		t.Fatalf("next: %+v %v", it, err)
	}
	// An edit above the section between selection and acceptance is harmless.
	out, n, err := MarkDone("intro\n"+markRoadmap, it)
	if err != nil || n != 2 {
		t.Fatalf("mark: %d %v", n, err)
	}
	if !strings.Contains(out, "- [x] b\n  - [x] c\n") || !strings.Contains(out, "## P2 Вторая\n- [ ] d\n") {
		t.Fatalf("wrong flip:\n%s", out)
	}
	next, err := Next(out)
	if err != nil || next.ID != "P2" {
		t.Fatalf("after mark next must be P2: %+v %v", next, err)
	}
	if _, _, err := MarkDone(strings.Replace(markRoadmap, "P1 Первая", "P1 renamed", 1), it); err != ErrPhaseMoved {
		t.Fatalf("renamed heading must refuse: %v", err)
	}
}

func TestMarkDoneFileIsAtomicAndKeepsMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(p, []byte(markRoadmap), 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	it, _ := Next(markRoadmap)
	if n, err := MarkDoneFile(p, it); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	st, _ := os.Stat(p)
	raw, _ := os.ReadFile(p)
	if st.Mode().Perm() != before.Mode().Perm() || strings.Contains(string(raw[:strings.Index(string(raw), "## P2")]), "[ ]") {
		t.Fatalf("mode %v content %q", st.Mode(), raw)
	}
	if n, err := MarkDoneFile(p, it); err != nil || n != 0 {
		t.Fatalf("second mark is a no-op: %d %v", n, err)
	}
}
