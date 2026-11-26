package roadmapitem

import "testing"

func TestNextSkipsOnlySupersededItemsWithDecision(t *testing.T) {
	const raw = "## P0 — Baseline\n- [~] P0.1 Prior exit <!-- superseded: knowledge:m-5684ad315011 -->\n- [~] P0.2 Missing decision\n"
	o, err := Next(raw)
	if err != nil || o.Item == nil || o.Item.ID != "P0.2" || o.Progress != "0/2" {
		t.Fatalf("next: %+v, %v", o, err)
	}
	if _, _, _, err := Close(raw, []string{"P0.1"}, "26.11.2026", ""); err == nil {
		t.Fatal("superseded item was accepted as completed")
	}
}
