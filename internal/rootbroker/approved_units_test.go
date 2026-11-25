package rootbroker

import (
	"fmt"
	"testing"
)

// MCP-114: a release touching up to 12 units is fully verified; 13 is refused.
func TestApprovedChecksUnitLimit(t *testing.T) {
	units := func(n int) []string {
		var u []string
		for i := 0; i < n; i++ {
			u = append(u, fmt.Sprintf("lane-part%d.service", i))
		}
		return u
	}
	if err := validateApprovedChecks(ApprovedPlan{Units: units(12)}); err != nil {
		t.Fatalf("12 units refused: %v", err)
	}
	if err := validateApprovedChecks(ApprovedPlan{Units: units(13)}); err == nil {
		t.Fatal("13 units accepted")
	}
}
