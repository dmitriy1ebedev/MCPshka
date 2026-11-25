package projectci

import (
	"fmt"
	"strings"
	"testing"
)

func TestMCP253PhaseGateRequiresExactSuccessfulRootCIReceipt(t *testing.T) {
	head := strings.Repeat("a", 40)
	for _, tc := range []struct {
		state, version, commit, kind string
		want                         bool
	}{
		{"SUCCEEDED", "0.17.0", head, "github", true},
		{"SUCCEEDED", "0.17.0", head, "gitverse", true},
		{"SUCCEEDED", "0.17.0", head, "module", true},
		{"SUCCEEDED", "0.17.0", head, "runner", false},
		{"SUCCEEDED", "0.17.0", head, "", false},
		{"APPLYING", "0.17.0", head, "github", false},
		{"ROLLED_BACK", "0.17.0", head, "github", false},
		{"SUCCEEDED", "0.17.1", head, "github", false},
		{"SUCCEEDED", "0.17.0", strings.Repeat("b", 40), "github", false},
	} {
		raw := fmt.Appendf(nil, `{"state":%q,"version":%q,"commit":%q,"test_evidence":%q}`, tc.state, tc.version, tc.commit, tc.kind)
		if got := releaseCIEvidence(raw, head, "0.17.0"); (got != "") != tc.want {
			t.Errorf("%s: %q", raw, got)
		}
	}
}
