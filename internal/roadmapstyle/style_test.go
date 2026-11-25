package roadmapstyle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplatePassesAndDeviationsAreNamed(t *testing.T) {
	if f := Check(Template, 0); len(f) != 1 || f[0].Rule != "version" {
		// The template row is a placeholder «0.N.M»: only that is reported.
		t.Fatalf("template: %v", f)
	}
	bad := "# X\n## Очередь\nделаем мспишку и Gitverse, «мспишка» в цитате можно, `provider=gitverse` код\n## Релизы\n| Версия | Дата | Статус |\n|---|---|---|\n| 0.4.54-rc.9 | 2026-11-01 | в проде |\n| 1.3.1 | 2026-11-10 | готово |\n"
	got := []string{}
	for _, f := range Check(bad, 0) {
		got = append(got, f.String())
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"«мспишку» → MCPshka", "«Gitverse» → GitVerse", "0.4.54-rc.9", "1.3.1: статус «готово»"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if len(got) != 4 {
		t.Errorf("quotes and code must not be graded:\n%s", joined)
	}
}

// TestTrackedDocsSpelling is the CI orthography gate of this repository's
// own documents (ROADMAPs are local and graded by project_context/phase_gate).
func TestTrackedDocsSpelling(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{"README.md", "README.ru.md", "CHANGELOG.md", "BUGLOG.md"}
	for _, pattern := range []string{"docs/*.md", "docs/releases/*.md", "internal/agentguide/guide/*.md"} {
		m, _ := filepath.Glob(filepath.Join(root, pattern))
		for _, f := range m {
			rel, _ := filepath.Rel(root, f)
			files = append(files, rel)
		}
	}
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(root, f))
		if os.IsNotExist(err) && f == "README.ru.md" {
			// Sealed snapshots of a controller older than 0.19.2 do not
			// carry README.ru.md yet (0.19.2 ship, 25.11).
			t.Logf("%s not in this snapshot: not graded", f)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range Spelling(string(raw)) {
			t.Errorf("%s: %s", f, finding)
		}
	}
}
