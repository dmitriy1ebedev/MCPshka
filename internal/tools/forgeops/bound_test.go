package forgeops

import (
	"strings"
	"testing"

	"mcpshka/internal/toolforge"
)

func TestBoundFindingsStaysWithinBudget(t *testing.T) {
	var all []toolforge.Finding
	for i := 0; i < 2000; i++ {
		all = append(all, toolforge.Finding{Kind: "overlap", Tools: []string{"a", "b"}, Advice: strings.Repeat("x", 200)})
	}
	kept, omitted := boundFindings(all)
	if omitted == 0 || len(kept)+omitted != len(all) || len(kept) == 0 {
		t.Fatalf("kept %d omitted %d", len(kept), omitted)
	}
	if k, o := boundFindings(all[:3]); len(k) != 3 || o != 0 {
		t.Fatal("a small audit is not cut")
	}
}
