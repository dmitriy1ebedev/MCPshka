package rootbroker

import (
	"context"
	"testing"
)

func TestCodexIncompleteMetadataKeepsKnownIDAcrossRestart(t *testing.T) {
	root, p := codexIntentFixture(t, false)
	calls := 0
	client := func(context.Context, any) (codexClientResult, error) {
		calls++
		return codexClientResult{ThreadID: "thread_created123", PolicyVerified: false}, nil
	}
	out, err := executeCodexSessionIntent(context.Background(), root, p, client)
	if err != nil || out.State != "HOLD" || out.ThreadID != "thread_created123" || out.RetryAllowed {
		t.Fatalf("incomplete metadata lost identity: %+v %v", out, err)
	}
	if out.Diagnostic == nil || out.Diagnostic.Stage != "policy_check" || out.Diagnostic.ErrorClass != "POLICY_UNCONFIRMED" {
		t.Fatal(out)
	}
	cold, err := readCodexSessionIntent(root, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err = executeCodexSessionIntent(context.Background(), root, cold, client)
	if err != nil || out.State != "HOLD" || out.ThreadID != "thread_created123" || calls != 1 {
		t.Fatalf("cold HOLD replayed creation: %+v calls=%d %v", out, calls, err)
	}
}
