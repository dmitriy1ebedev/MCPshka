package offbox

import "testing"

func TestGigaIDEBudgetMustCoverSession(t *testing.T) {
	for _, tc := range []struct {
		remaining string
		want      bool
	}{{"", false}, {"unknown", false}, {"0", false}, {"1", false}, {"2", true}, {"NaN", false}, {"-1", false}} {
		t.Setenv("MCPSHKA_GIGAIDE_VCPU_HOURS_REMAINING", tc.remaining)
		if got := gigaBudget(1800); got != tc.want {
			t.Fatal(tc.remaining, got)
		}
	}
}
