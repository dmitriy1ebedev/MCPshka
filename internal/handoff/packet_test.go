package handoff

import (
	"encoding/json"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDurablePacketTrustAndQuotaSwitch(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := Packet{Objective: "fixture", Scope: "/private/topology", HEAD: strings.Repeat("a", 40), Owner: "owner", RunID: "run-1", Checkpoint: "checkpoint-1", NextSafeAction: "reconcile original job", Stage: "UNKNOWN", ThreadID: "thread-123", SessionTreeID: "tree-123", JobID: "job-1", CreationKey: "key-1", Generation: 1, Fields: map[string]Field{"topology": {"/private", "untrusted", false}, "note": {"Bearer SECRET", "untrusted", false}}}
	if err := Save(db, "packet", p); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(db, "packet", "mock", time.Now()); err == nil {
		t.Fatal("implicit trust")
	}
	if err := SetPolicy(db, Policy{"mock", "untrusted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(db, "packet", "mock", time.Now()); err == nil {
		t.Fatal("implicit authentication")
	}
	now := time.Now()
	if err := ObserveAuthentication(db, "mock", true, now); err != nil {
		t.Fatal(err)
	}
	out, err := Export(db, "packet", "mock", now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "SECRET") || out["stage"] != "UNKNOWN" || out["retry_allowed"] != false {
		t.Fatal(string(raw))
	}
	stored, err := Read(db, "packet")
	if err != nil {
		t.Fatal(err)
	}
	switched, err := Switch(db, "packet", "second", stored.PlanFingerprint, true)
	if err != nil || switched.ThreadID != stored.ThreadID || switched.JobID != stored.JobID || switched.Stage != "UNKNOWN" || switched.CreationKey != stored.CreationKey || switched.PlanFingerprint != stored.PlanFingerprint {
		t.Fatal(switched, err)
	}
	if _, err := Switch(db, "packet", "third", stored.PlanFingerprint, false); err == nil {
		t.Fatal("unfenced switch")
	}
	if _, err := Export(db, "packet", "mock", now.Add(6*time.Minute)); err == nil {
		t.Fatal("stale auth")
	}
}
