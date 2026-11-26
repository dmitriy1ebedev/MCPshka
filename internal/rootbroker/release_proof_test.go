package rootbroker

import (
	"context"
	"testing"
)

func TestReleaseProofRejectsNonFixedArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"../secrets"}, {"1793950000000-123456789abc", "extra"}, {"--help"}, {"1793950000000-ABCDEFABCDEF"}} {
		if _, err := runReleaseProof(context.Background(), args); err == nil {
			t.Fatal("unfixed proof request accepted", args)
		}
	}
}
