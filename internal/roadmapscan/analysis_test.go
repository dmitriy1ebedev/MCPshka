package roadmapscan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ROADMAP P24: progress in numbers and every stale or contradicting
// acceptance mark, without accepting anything.
func TestAnalyzeProgressAndInconsistencies(t *testing.T) {
	doc := `# Roadmap

| Фаза | Тир | x | o | Статус |
|---|---|---|---|---|
| P0 — Base | 1 | 2 | 0 | DONE |
| P1 — Core | 1 | 0 | 5 | OPEN |

## P0 — Base — DONE
- [x] one
- [ ] two

## P1 — Core
- [x] one
- [x] two

## P2 — Later

## P3 — Clean
- [x] one
- [ ] two
`
	path := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, a, err := Analyze(path, "")
	if err != nil || len(snap.Phases) != 4 {
		t.Fatalf("%+v %v", snap, err)
	}
	want := Analysis{Phases: 4, PhasesDone: 1, Checks: 6, ChecksDone: 4, Percent: 66, Inconsistent: []string{
		"P0: declared DONE with 1 open items",
		"P1: all 2 items done but the phase is not marked DONE (acceptance missing)",
		"P2: no acceptance items",
		"P0: status table says 2 done / 0 open, the section has 1 / 1",
		"P1: status table says 0 done / 5 open, the section has 2 / 0",
	}}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("analysis:\n%+v", a)
	}
	if _, _, err := Analyze(path, "0000"); err != ErrPinMismatch {
		t.Fatalf("pin: %v", err)
	}
}
