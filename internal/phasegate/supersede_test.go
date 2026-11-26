package phasegate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func TestSupersedeRetainsPendingEvidenceAndPinsOnlyOneCriterion(t *testing.T) {
	raw := []byte(`{"gates":{"device":{"status":"PENDING","note":"awaiting hardware","min_duration_seconds":90},"unrelated":"FAIL"}}`)
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	in := SupersedeInput{Criterion: "device", CheckpointSHA: hash, PreviousRoadmapSHA: hash, RoadmapSHA: fmt.Sprintf("%064x", 1), Decision: "knowledge:m-5684ad315011", Successor: "issue #41"}
	doc, err := reviseCheckpoint(raw, in, "owner", time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	g := doc.Gates["device"]
	if g.Status != "SUPERSEDED" || g.Supersession == nil || g.Supersession.Prior.Status != "PENDING" || g.Supersession.Prior.Note != "awaiting hardware" || g.Supersession.Prior.MinDurationSeconds != 90 || doc.Gates["unrelated"].Status != "FAIL" || PassingGate(g.Status) {
		t.Fatalf("unexpected gates: %+v", doc.Gates)
	}
	saved, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var reopened CheckpointDoc
	if err := json.Unmarshal(saved, &reopened); err != nil || !reopened.Gates["unrelated"].legacy {
		t.Fatalf("legacy criterion changed: %+v %v", reopened, err)
	}
	state := t.TempDir()
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(filepath.Join(state, "jobs"), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	db := manager.Database()
	change := reopened.Gates["device"].Supersession
	if verifiedSupersession(db, "/r", "P1", "device", change) {
		t.Fatal("unauthorized supersession verified")
	}
	if err := recordSupersession(db, "/r", "P1", "device", change); err != nil {
		t.Fatal(err)
	}
	if !verifiedSupersession(db, "/r", "P1", "device", change) || verifiedSupersession(db, "/r", "P1", "other", change) {
		t.Fatal("revision receipt was not scoped to the exact criterion")
	}

	oldRoot := CheckpointStateRoot
	CheckpointStateRoot = state
	t.Cleanup(func() { CheckpointStateRoot = oldRoot })
	st := gitops.StatusResult{Root: "/r"}
	path := CheckpointPaths(st.Root, "P1")[0]
	var digest string
	if err := saveCheckpoint(path, reopened); err != nil {
		t.Fatal(err)
	}
	gate := phaseCheckpointCheck(nil, manager, st, "", "P1", true, &digest)
	if gate.Status != "FAIL" || !strings.Contains(gate.Detail, "unrelated=FAIL") || strings.Contains(gate.Detail, "device") {
		t.Fatalf("unrelated criterion was not enforced: %+v", gate)
	}
	// The same record on another phase must not be trusted.
	if err := saveCheckpoint(CheckpointPaths(st.Root, "P2")[0], reopened); err != nil {
		t.Fatal(err)
	}
	gate = phaseCheckpointCheck(nil, manager, st, "", "P2", true, &digest)
	if gate.Status != "FAIL" || !strings.Contains(gate.Detail, "device=SUPERSEDED") {
		t.Fatalf("foreign revision was trusted: %+v", gate)
	}
	in.CheckpointSHA = in.RoadmapSHA
	if _, err := reviseCheckpoint(raw, in, "owner", time.Unix(100, 0)); err == nil {
		t.Fatal("stale checkpoint accepted")
	}
}
