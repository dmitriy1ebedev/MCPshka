package roadmapitem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shape of Lane's roadmap on 11.11.2026: an older numbered section with
// a [-] item above the phase the owner named as next.
const lane = `# Lane Roadmap

## 1.4.0 — 20 пунктов
19. [x] **V. Инвентарь**
20. [-] **L2. Сторож** живой случай не проверен.

### Приоритет G0 — перевод на Go [БЛИЖАЙШИЙ]

Порядок — «душитель».

- [ ] **GO0. Каркас.** бинарь ` + "`lane-go`" + `. Exit: ` + "`lane version`" + ` из Go, откат — убрать команду из списка.
- [ ] **GO1. Чистые модули** сверка байт-в-байт.

**Обязательные тесты (без них пункт не закрывается):**
1. go test и go vet в CI.

### Приоритет R1 — UDP
- [x] Доказать UDP в лаборатории.
- [ ] Приёмка на клиенте.
- Exit: голос Discord работает.
`

func TestNextIsOneCheckboxOfTheCurrentPhase(t *testing.T) {
	o, err := Next(lane)
	if err != nil {
		t.Fatal(err)
	}
	if o.Phase != "G0" || o.Item == nil || o.Item.ID != "GO0" || o.Progress != "0/2" {
		t.Fatalf("want G0/GO0 0/2, got %+v item %+v", o, o.Item)
	}
	if !strings.HasPrefix(o.Item.Exit, "`lane version` из Go") {
		t.Fatalf("exit: %q", o.Item.Exit)
	}
	if len(o.AlsoOpen) != 1 || o.AlsoOpen[0] != "GO1" || !strings.Contains(o.PhaseRules, "Обязательные тесты") || strings.Contains(o.PhaseRules, "UDP") {
		t.Fatalf("also %v rules %q", o.AlsoOpen, o.PhaseRules)
	}
	if o.Warning != "" || len(o.Stages) != 6 || !strings.Contains(o.DoneWhen, "roadmap_close") {
		t.Fatalf("order text: %+v", o)
	}
}

func TestCloseMarksOnlyNamedItems(t *testing.T) {
	out, closed, already, err := Close(lane, []string{"GO0", "R1.1"}, "12.11.2026", "job:abc")
	if err != nil || len(closed) != 1 || closed[0] != "GO0" || len(already) != 1 {
		t.Fatalf("%v %v %v", closed, already, err)
	}
	if !strings.Contains(out, "- [x] **GO0. Каркас.**") || !strings.Contains(out, "убрать команду из списка. — закрыт 12.11.2026: job:abc\n") || !strings.Contains(out, "- [ ] **GO1.") {
		t.Fatalf("wrong flip:\n%s", out)
	}
	o, err := Next(out)
	if err != nil || o.Item.ID != "GO1" || o.Progress != "1/2" {
		t.Fatalf("after close: %+v %v", o, err)
	}
	// The phase exit line is the criterion of an item that has none.
	out, _, _, _ = Close(out, []string{"GO1"}, "12.11.2026", "")
	if o, _ = Next(out); o.Phase != "R1" || o.Item.ID != "R1.2" || o.Item.Exit != "голос Discord работает." {
		t.Fatalf("R1: %+v %+v", o, o.Item)
	}
	if _, _, _, err := Close(lane, []string{"GO9"}, "d", ""); err == nil {
		t.Fatal("unknown item must be refused")
	}
}

func TestQueueWithoutPhasesAndMissingExit(t *testing.T) {
	o, err := Next("# R\n\n## Очередь\n1. [x] **A1. Было**\n2. [ ] Сделать без критерия\n")
	if err != nil || o.Phase != "Q" || o.Item.ID != "Q.2" || !strings.Contains(o.Warning, "Exit") {
		t.Fatalf("%+v %v", o, err)
	}
	if _, err := Next("# R\n\n## P1 — A\n- [x] a\n"); err == nil {
		t.Fatal("finished roadmap has no next item")
	}
	if o, err := Next("# R\n\n## P1 — A\n\nтекст без пунктов\n"); err != nil || o.Item != nil || o.Warning == "" {
		t.Fatalf("phase without checkboxes must ask for items: %+v %v", o, err)
	}
}

func TestCloseFileKeepsModeAndRefusesNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(p, []byte(lane), 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	closed, _, err := CloseFile(p, []string{"GO0"}, "12.11.2026", "")
	st, _ := os.Stat(p)
	if err != nil || len(closed) != 1 || st.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("%v %v %v", closed, err, st.Mode())
	}
	closed, already, err := CloseFile(p, []string{"GO0"}, "12.11.2026", "")
	if err != nil || len(closed) != 0 || len(already) != 1 {
		t.Fatalf("second close is a no-op: %v %v %v", closed, already, err)
	}
}

func TestCloseAcceptedRefusesUnacceptedItemsWithoutEffect(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(p, []byte(lane), 0o600); err != nil {
		t.Fatal(err)
	}
	commits := 0
	commit := func(string) (string, error) { commits++; return "abc", nil }
	in := CloseInput{Items: []string{"GO0", "GO1"}, Note: "1.6.0, job:1"}
	gate := func(id string) string {
		if id == "GO1" {
			return "checkpoint: no checkpoint for GO1"
		}
		return ""
	}
	_, err := CloseAccepted(in, p, nil, gate, commit)
	if raw, _ := os.ReadFile(p); err == nil || !strings.Contains(err.Error(), "GO1") || string(raw) != lane || commits != 0 {
		t.Fatalf("one unaccepted item must refuse the whole batch: %v", err)
	}
	if _, err := CloseAccepted(in, p, errors.New("stale"), func(string) string { return "" }, commit); err == nil || commits != 0 {
		t.Fatal("no lease, no close")
	}
	res, err := CloseAccepted(in, p, nil, func(string) string { return "" }, commit)
	if err != nil || len(res.Closed) != 2 || res.Commit != "abc" || commits != 1 || res.Next == nil || res.Next.Phase != "R1" {
		t.Fatalf("%+v %v", res, err)
	}
	if err := os.WriteFile(p, []byte(lane), 0o600); err != nil {
		t.Fatal(err)
	}
	if o, _ := NextFile("/r", p, 60); !strings.Contains(o.Warning, "60 коммитов") || o.Roadmap != p {
		t.Fatalf("warning: %q", o.Warning)
	}
}
// MCP-278: the 0.19.0 acceptance recorded checkpoints for items of the S5
// stream (S5.13, K2.1…), but roadmap_close only saw "## P" phases.
const streams = "# R\n\n## S5 — Каталог\n\n- [ ] первый\n- [ ] S5.3 явный\n- [x] второй по счёту\n\n### S5.CM — Code Mode\n\n- [ ] третий\n- [ ] K2.1 память\n\n## P0 — База\n\n- [ ] пункт фазы\n"

func TestStreamItemsAreClosable(t *testing.T) {
	ids := map[string]string{}
	for _, p := range Parse(streams) {
		for _, e := range p.Entries {
			ids[e.ID] = p.ID
		}
	}
	for id, phase := range map[string]string{"S5.1": "S5", "S5.3": "S5", "S5.3a": "S5", "S5.4": "S5", "K2.1": "S5", "P0.1": "P0"} {
		if ids[id] != phase {
			t.Fatalf("%s: phase %q, ids %v", id, ids[id], ids)
		}
	}
	out, closed, _, err := Close(streams, []string{"S5.4", "K2.1"}, "25.11.2026", "0.19.1")
	if err != nil || len(closed) != 2 || !strings.Contains(out, "- [x] третий — закрыт 25.11.2026: 0.19.1") || !strings.Contains(out, "- [x] K2.1 память") {
		t.Fatalf("close: %v %v\n%s", closed, err, out)
	}
	if o, err := Next(streams); err != nil || o.Phase != "P0" {
		t.Fatalf("next must stay on the P phase: %+v %v", o, err)
	}
}
