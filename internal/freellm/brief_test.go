package freellm

import "testing"

func TestBriefCountsOnlyGreenAsWaiting(t *testing.T) {
	dir := t.TempDir()
	c := Conveyor{Dir: dir}
	if Brief(dir) != "" {
		t.Fatal("empty queue must be silent")
	}
	for _, f := range []Fix{{Card: "MCP-101", State: "blocked"}, {Card: "MCP-102", State: "blocked"}} {
		if err := c.save(f); err != nil {
			t.Fatal(err)
		}
	}
	if got := Brief(dir); got != "FreeLLM: без патча 2" {
		t.Fatalf("blocked only: %q", got)
	}
	if err := c.save(Fix{Card: "MCP-103", State: "green"}); err != nil {
		t.Fatal(err)
	}
	if got := Brief(dir); got != "FreeLLM: ждут проверки 1 · fixes_review, без патча 2" {
		t.Fatalf("with green: %q", got)
	}
}
