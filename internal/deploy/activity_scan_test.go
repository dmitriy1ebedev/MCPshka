package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/statestore"
)

func TestReconcileScansHistoryBeyondLegacyCap(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The old per-record reader held admission at 129 historical receipts.
	for range 140 {
		id, err := deploymentID()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(Status{DeploymentID: id, State: "succeeded", StartedAt: "2026-01-01T00:00:00Z", FinishedAt: "2026-01-01T00:01:00Z"})
		if err = db.Put("deploys/"+id+".json", raw); err != nil {
			t.Fatal(err)
		}
	}
	out, err := New(nil, filepath.Join(root, "deploys")).Reconcile()
	if err != nil || out.Pending || out.NonterminalCount != 0 {
		t.Fatalf("history scan=%+v err=%v", out, err)
	}
}

func TestRootWorkerHistoricalProofUsesInProcessVerifier(t *testing.T) {
	saved := historicalProofSource
	t.Cleanup(func() { historicalProofSource = saved })
	called := false
	UseRootHistoricalProof(func(_ context.Context, args []string) ([]byte, error) {
		called = len(args) == 3 && args[0] == "1700000000000-0123456789ab"
		return nil, errors.New("verifier reached")
	})
	_, err := rootHistoricalProof(Status{DeploymentID: "1700000000000-0123456789ab"}, []byte("raw"), []byte("fence"))
	if !called || err == nil || !strings.Contains(err.Error(), "attestation failed: HOLD") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
