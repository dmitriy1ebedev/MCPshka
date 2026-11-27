package distributedgate

import (
	"strings"
	"testing"
)

func TestBootstrapPreservesPermitIdentities(t *testing.T) {
	for _, tc := range []struct {
		name   string
		derive func(string, string, string) string
		want   string
	}{
		{"module", ModuleTestBootstrap, "4f7a78f25896066b2328a44f2c1de4a8"},
		{"release", LocalReleaseBootstrap, "32b3d2aaf33576f337b60460280afce8"},
		{"ci", CIEvidenceBootstrap, "6dff6c90650fbbe665573c5ebe0575a3"},
		{"gitverse", GitVerseEvidenceBootstrap, "72c8d3564f8e7fdd8b35360ca342cc36"},
		{"distributed", DistributedEvidenceBootstrap, "a8f64574de8ded3e7da0df371cf28193"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, commit := "/root/repos/mcpshka", strings.Repeat("a", 40)
			for _, attempt := range []string{"", "1"} {
				if got := tc.derive(repo, commit, attempt); got != tc.want {
					t.Fatalf("attempt %q: %s, want %s", attempt, got, tc.want)
				}
			}
			if tc.derive(repo, commit, "2") == tc.want {
				t.Fatal("new attempt reused the permit")
			}
		})
	}
}
