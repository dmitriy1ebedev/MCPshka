package rootbroker

import (
	"strings"
	"testing"
)

func TestRetirementReasonV1PreservesPreWorkerBounds(t *testing.T) {
	for _, s := range []string{"", "  ", strings.Repeat("x", 201), strings.Repeat("я", 101), "line\nbreak", "line\rbreak", "nul\x00byte", "-option"} {
		if ValidateRetirementReasonV1(s) == nil {
			t.Fatalf("invalid fixed reason accepted: %q", s)
		}
	}
	for _, s := range []string{"Approved retirement", strings.Repeat("x", 200), strings.Repeat("я", 100)} {
		if err := ValidateRetirementReasonV1(s); err != nil {
			t.Fatal(err)
		}
	}
}
