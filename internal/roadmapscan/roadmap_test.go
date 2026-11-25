package roadmapscan

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const roadmap = "# Tier 1\n## P0 — Baseline\n- [x] complete\n## P3 — State Store — DONE (2026-10-08)\n- [x] complete\n## P4 — Survivability\n- [x] baseline\n- [ ] OOB validation\n## P7 — Workflow\n- [ ] inventory\n"

func TestLoadExpandedRoadmapRetainsPinAndAcceptanceSemantics(t *testing.T) {
	content := strings.Repeat("# advisory context\n", 12000) + roadmap
	if len(content) <= 128*1024 || len(content) > 256*1024 {
		t.Fatal("fixture must exercise the expanded bounded document range")
	}
	path := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	snapshot, err := Load(path, want)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SHA256 != want || len(snapshot.Phases) != 4 || snapshot.Next == nil || snapshot.Next.ID != "P4" || snapshot.Phases[2].DeclaredDone {
		t.Fatalf("expanded document changed acceptance or evidence: %+v", snapshot)
	}
	if err := os.WriteFile(path, []byte(content+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, want); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("lost stale-pin rejection: %v", err)
	}
}

func TestParseNextAndEvidence(t *testing.T) {
	s, e := Parse(roadmap, "pinned")
	if e != nil {
		t.Fatal(e)
	}
	if s.Next == nil || s.Next.ID != "P4" {
		t.Fatalf("next = %+v", s.Next)
	}
	if len(s.Phases) != 4 || s.Phases[2].Total != 2 || s.Phases[2].Completed != 1 {
		t.Fatalf("phases = %+v", s.Phases)
	}
	if !s.Phases[1].DeclaredDone {
		t.Fatal("expected explicit DONE annotation")
	}
	if s.Phases[0].DeclaredDone {
		t.Fatal("checked checkboxes must not imply acceptance")
	}
}
func TestRejectAmbiguousInputs(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate":          "## P1 — One\n## P1 — Two\n",
		"no phases":          "# Draft\n- [ ] TODO\n",
		"unterminated fence": "## P1 — OK\n```\n## P2 — False\n",
		"too many checks":    "## P1 — OK\n" + strings.Repeat("- [ ] item\n", MaxChecks+1),
		"invalid UTF-8":      "## P1 — \xff\n- [ ] verify\n",
		"oversized":          "## P1 — OK\n" + strings.Repeat(" ", MaxBytes),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := Parse(raw, ""); e == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
func TestIgnoreUntrustedFencedText(t *testing.T) {
	raw := "## P1 — First — DONE\n```md\n## P2 — DO NOT RUN\n- [ ] dangerous\n```\n## P3 — Actual\n- [ ] task\n"
	s, e := Parse(raw, "")
	if e != nil || len(s.Phases) != 2 || s.Next.ID != "P3" {
		t.Fatalf("snapshot=%+v err=%v", s, e)
	}
}
func TestLoadPinAndSymlink(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ROADMAP.md")
	if e := os.WriteFile(p, []byte(roadmap), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := Load(p, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Load(p, s.SHA256); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(roadmap+"- [ ] new\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(p, s.SHA256); !errors.Is(e, ErrPinMismatch) {
		t.Fatalf("pin check = %v", e)
	}
	link := filepath.Join(dir, "link.md")
	if e := os.Symlink(p, link); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(link, ""); e == nil {
		t.Fatal("symlink should fail")
	}
}
func TestDeferredItemsRemainVisibleButAreNotAutoSelected(t *testing.T) {
	raw := "## P0 — VPS lab\n- [ ] remote acceptance\n  - DEFERRED by user, do not auto-close\n## P4 — Rescue\n- [ ] staged safety\n"
	s, e := Parse(raw, "")
	if e != nil {
		t.Fatal(e)
	}
	if !s.Phases[0].Deferred || s.Phases[0].Completed != 0 {
		t.Fatalf("deferred phase=%+v", s.Phases[0])
	}
	if s.Next == nil || s.Next.ID != "P4" {
		t.Fatalf("next=%+v", s.Next)
	}
}
func TestNoCheckboxesStillRequiresAcceptance(t *testing.T) {
	raw := "## P0 — Completed — DONE (2026-10-08)\n## P5 — Host Control\nAcceptance requires service probes and audit.\n## P6 — Durable Jobs\n- [ ] recovery\n"
	s, e := Parse(raw, "")
	if e != nil {
		t.Fatal(e)
	}
	if s.Next == nil || s.Next.ID != "P5" {
		t.Fatalf("phase with no checklist must not be silently skipped: %+v", s.Next)
	}
}
func TestSizeLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.md")
	if e := os.WriteFile(p, []byte(strings.Repeat("x", MaxBytes+1)), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(p, ""); e == nil {
		t.Fatal("expected bound")
	}
}

func TestHeadroomWarnsBeforeTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(path, make([]byte, MaxBytes*8/10), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := Headroom(path); err != nil || r.Warn || r.Percent < 79 || r.Percent > 80 {
		t.Fatalf("80%%: %+v %v", r, err)
	}
	if err := os.WriteFile(path, make([]byte, MaxBytes*95/100), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := Headroom(path); err != nil || !r.Warn || r.Hint == "" {
		t.Fatalf("95%% must warn while merges still work: %+v %v", r, err)
	}
}

func TestParseColonHeadingsAsLaneWritesThem(t *testing.T) {
	raw := "# Lane Roadmap\n\nСтатусы: `[ ]`, `[-]`, `[x]`.\n\n## P0: Перевод legacy OpenVPN на Lane/INCY (01.11.2026)\n\nExit:\n- [x] Проверена идентичность outbound.\n- [ ] Точный immutable release.\n\n## P1: Next\n- [ ] later\n"
	s, err := Parse(raw, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Phases) != 2 || s.Next == nil || s.Next.ID != "P0" || s.Next.Total != 2 || s.Next.Completed != 1 {
		t.Fatalf("lane phases = %+v next=%+v", s.Phases, s.Next)
	}
	if s.Phases[0].Title != "Перевод legacy OpenVPN на Lane/INCY (01.11.2026)" {
		t.Fatalf("title = %q", s.Phases[0].Title)
	}
	for _, bad := range []string{"## P0:Title\n- [ ] x\n", "## P0 Title\n- [ ] x\n", "### P0: Title\n- [ ] x\n"} {
		if _, err := Parse(bad, ""); err == nil {
			t.Fatalf("ambiguous heading accepted: %q", bad)
		}
	}
}

func TestParseLetteredPhasesWhenNoPPhases(t *testing.T) {
	s, err := Parse("# Lane\n## Очередь\n### Приоритет L0 — baseline [BLOCKING]\n- [x] a\n### Приоритет L1 — adapter\n- [ ] b\n## I — не фаза\n", "sha")
	if err != nil || len(s.Phases) != 2 || s.Next == nil || s.Next.ID != "L1" {
		t.Fatalf("%+v %v", s, err)
	}
	p, err := Parse("## P1 — one\n- [ ] a\n## S1 — two\n- [ ] b\n", "sha")
	if err != nil || len(p.Phases) != 1 || p.Next.ID != "P1" {
		t.Fatalf("P-roadmap must keep its phases: %+v %v", p, err)
	}
}
