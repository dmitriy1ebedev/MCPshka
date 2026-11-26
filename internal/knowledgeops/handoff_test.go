package knowledgeops

import (
	"mcpshka/internal/handoff"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandoffUsesExistingKnowledgeAPI(t *testing.T) {
	state := t.TempDir()
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := jobs.New(filepath.Join(state, "jobs"), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	p := handoff.Packet{Objective: "fixture", Scope: "fixture", HEAD: strings.Repeat("a", 40), Owner: "owner", RunID: "run", Checkpoint: "checkpoint", NextSafeAction: "reconcile", Stage: "UNKNOWN"}
	if _, err := Write(m, KnowledgeWriteInput{Action: "handoff", ID: "packet", Packet: &p}); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(m, KnowledgeWriteInput{Action: "provider_trust", Provider: "mock", TrustTier: "untrusted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(m, KnowledgeReadInput{Action: "handoff", ID: "packet", Provider: "mock"}); err == nil {
		t.Fatal("unconfirmed auth exported")
	}
	if err := handoff.ObserveAuthentication(m.Database(), "mock", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	raw, err := Read(m, KnowledgeReadInput{Action: "handoff", ID: "packet", Provider: "mock"})
	if err != nil || !strings.Contains(string(raw), "\"stage\":\"UNKNOWN\"") {
		t.Fatal(string(raw), err)
	}
}
