package roadmapnext

import "testing"

func TestNextAcrossRoadmapFormats(t *testing.T) {
	diag := `# DIAG
## P02 — Portable Go core · ` + "`0.1.2`" + `
- [x] Go codebase
- [x] Static builds
**DONE:** cross-build smoke.

## P03 — Bounded execution & cancellation · ` + "`0.1.3`" + `
- [x] Per-probe deadlines
- [ ] No shell interpolation
**DONE:** hung child cancelled.

## P04 — Evidence schema
- [ ] Schema
`
	it, err := Next(diag)
	if err != nil || it.ID != "P03" || len(it.Open) != 1 || it.Done != 1 || it.Acceptance != "hung child cancelled." {
		t.Fatalf("DIAG next: %+v %v", it, err)
	}
	lane := `# Lane
## Как движется проект
text
## Единственная рабочая очередь
0. **[-] P0: удалённый доступ к приставке.**
   details
1. **[x] Done thing**
2. **[ ] Native iPhone path**
`
	it, err = Next(lane)
	if err != nil || it.ID != "P0" || len(it.Open) != 2 || it.Done != 1 {
		t.Fatalf("LANE next: %+v %v", it, err)
	}
	if _, err := Next("# Done\n## A\n- [x] a\n"); err == nil {
		t.Fatal("finished roadmap reported open work")
	}
}
