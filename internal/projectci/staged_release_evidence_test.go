package projectci

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mcpshka/internal/releaseprep"
)

func TestStagedReleaseEvidenceRechecksRootProof(t *testing.T) {
	dir := t.TempDir()
	head := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	hash := strings.Repeat("c", 64)
	r := releaseprep.LocalReceipt{
		Schema: releaseprep.LocalSchema, Provider: "h96-local", Repository: "dmitriy1ebedev/mcpshka",
		SourceSHA: head, HeadSHA: head, Tree: tree, SourceDigest: hash,
		BuildPermit: strings.Repeat("d", 32), BuildEnvDigest: hash, BinarySHA: hash, BrokerSHA: hash,
		TestEvidence: releaseprep.TestEvidence{
			Kind: "module", ID: strings.Repeat("e", 32), Tree: tree,
			Conclusion: "success", Suites: map[string]string{"module": "success"},
		},
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcpshka-0.17.2.cccccccccccc.candidate.ci-receipt.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	tampered := r
	tampered.BrokerSHA = strings.Repeat("f", 64)
	for _, tc := range []struct {
		name, head string
		proof      releaseprep.LocalReceipt
		wantCalls  int
		wantPass   bool
	}{
		{"wrong HEAD", strings.Repeat("f", 40), r, 0, false},
		{"mismatched root proof", head, tampered, 1, false},
		{"exact root proof", head, r, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got := checkStagedRelease(t.Context(), dir, tc.head, func(_ context.Context, family string, args ...string) ([]byte, error) {
				calls++
				if family != "executor" || !slices.Equal(args, []string{"release-receipt", r.BuildPermit, r.TestEvidence.ID}) {
					t.Fatalf("unexpected broker request: %s %v", family, args)
				}
				return json.Marshal(tc.proof)
			})
			if calls != tc.wantCalls || (got != "") != tc.wantPass {
				t.Fatalf("broker calls=%d evidence=%q", calls, got)
			}
		})
	}
}
