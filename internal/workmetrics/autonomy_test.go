package workmetrics

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/audit"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/mission"
	"mcpshka/internal/statestore"
)

func TestAutonomyMeasuresEvidenceAndScopedReceipts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := statestore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.New(filepath.Join(dir, "audit.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	const project = "/root/repos/example"
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	start := now.Add(-time.Hour)
	report := func(p string) Autonomy {
		return (Sources{DB: db, Audit: log}).Measure(ctx, Period{Since: start.Add(-time.Second), Until: now, Project: p, StuckSeconds: 900}, now)
	}
	put := func(key string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Put(key, raw); err != nil {
			t.Fatal(err)
		}
	}
	logEvent := func(e audit.Event) {
		t.Helper()
		if err := log.Log(e); err != nil {
			t.Fatal(err)
		}
	}
	s := mission.Snapshot{
		Plan: mission.Plan{Path: project, Steps: []mission.Step{{ID: "step"}}},
		ID:   "one", State: "DONE", Current: 1,
		History:     []mission.Event{{Action: "start", At: start}, {Action: "resume", At: start.Add(time.Minute)}, {State: "DONE", At: start.Add(10 * time.Minute)}},
		Checkpoints: []mission.Checkpoint{{Step: "step", At: start.Add(5 * time.Minute), Gates: []string{"acceptance"}}},
	}
	put("missions/one", s)
	s.ID, s.Checkpoints = "two", nil
	put("missions/two", s)
	s.Plan.Path = "/root/repos/other"
	put("missions/other", s)
	for _, e := range []audit.Event{
		{Type: SessionCall, Time: start.Add(-time.Second), Command: "mission_start", Workdir: project, Details: map[string]any{"session": "session", "mission": "one"}},
		{Type: SessionCall, Time: start.Add(time.Minute), Command: "mission_resume", Workdir: project, Result: "ok", Details: map[string]any{"session": "session", "mission": "one"}},
		{Type: SessionCall, Time: start.Add(2 * time.Minute), Command: "admin_session_begin", Workdir: project, Details: map[string]any{"session": "session", "approval": "challenge"}},
		{Type: SessionCall, Time: start.Add(11 * time.Minute), Command: "admin_session_begin", Workdir: project, Details: map[string]any{"session": "session", "approval": "challenge"}},
		{Type: "mutation_contract", Time: start, Workdir: project, Details: map[string]any{"receipt_key_sha256": "one"}},
		{Type: "mutation_contract", Time: start, Workdir: project, Details: map[string]any{"receipt_key_sha256": "two"}},
		{Type: "mutation_contract", Time: start, Workdir: project, Result: "rejected_no_effect", Details: map[string]any{"receipt_key_sha256": "discarded"}},
	} {
		logEvent(e)
	}
	for _, key := range []string{"one", "two", "other"} {
		put("idempotency/"+key, idempotency.Entry{KeyHash: key, Operation: "contract/fs_write", Fingerprint: "identical", State: idempotency.Pending, StartedAt: start})
	}
	got := report(project)
	if got.Missions != 2 || got.Accepted != 1 || got.DoneWithoutEvidence != 1 || got.StuckUnknownPending != 2 || got.DuplicateMutationKeys != 1 || got.RunsWithoutEffect != 1 {
		t.Fatalf("wrong counts: %+v", got)
	}
	if got.OwnerNudges == nil || *got.OwnerNudges != 1 || got.FaceIDRequests == nil || *got.FaceIDRequests != 1 {
		t.Fatalf("wrong session metrics: %+v", got)
	}
	for name, metric := range map[string]struct {
		Value Average
		Want  float64
	}{
		"calls": {got.CallsPerAccepted, 3}, "first": {got.FirstResultSeconds, 300}, "accepted": {got.AcceptanceSeconds, 600}, "auto": {got.WithoutManualResume, 0},
	} {
		if metric.Value.Samples != 1 || metric.Value.Mean == nil || *metric.Value.Mean != metric.Want {
			t.Errorf("%s: %+v, want %v", name, metric.Value, metric.Want)
		}
	}
	if got := report(""); got.Missions != 3 || got.DuplicateMutationKeys != 2 || got.StuckUnknownPending != 3 {
		t.Fatalf("wrong global totals: %+v", got)
	}
}

func TestAutonomyUnavailableSourcesAreExplicit(t *testing.T) {
	now := time.Now()
	got := (Sources{}).Measure(context.Background(), Period{Since: now.Add(-time.Hour), Until: now}, now)
	if got.OwnerNudges != nil || got.FaceIDRequests != nil || got.CallsPerAccepted.Mean != nil || len(got.Notes) == 0 {
		t.Fatalf("unknown metrics became measured zero: %+v", got)
	}
}
