package workmetrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

func TestComputeCountsWhatNeedsAHand(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return `"` + now.Add(-d).Format(time.RFC3339) + `"` }
	for k, v := range map[string]string{
		"missions/a.json":   `{"state":"DONE"}`,
		"missions/b.json":   `{"state":"RUNNING"}`,
		"missions/c.json":   `{"state":"BLOCKED"}`,
		"missions/d.json":   `{"state":"DONE"}`,
		"leases/old.json":   `{"project_root":"/root/repos/lane","owner":"codex","heartbeat_at":` + ts(3*time.Hour) + `}`,
		"leases/fresh.json": `{"project_root":"/root/repos/diag","owner":"claude","heartbeat_at":` + ts(time.Minute) + `}`,
		"idempotency/x":     `{"operation":"project_commit","state":"pending","started_at":` + ts(time.Hour) + `}`,
		"idempotency/y":     `{"operation":"project_commit","state":"pending","started_at":` + ts(time.Minute) + `}`,
		"idempotency/z":     `{"operation":"project_test","state":"completed","started_at":` + ts(time.Hour) + `}`,
	} {
		if err := db.Put(k, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	w := Compute(context.Background(), db, now)
	if !w.Complete || w.Missions.Started != 4 || w.Missions.Accepted != 2 || w.Missions.Running != 1 || w.Missions.Stopped != 1 || w.Missions.Ratio != 0.5 {
		t.Fatalf("missions: %+v (%s)", w.Missions, w.Note)
	}
	if len(w.StrandedLeases) != 1 || w.StrandedLeases[0].Owner != "codex" {
		t.Fatalf("stranded: %+v", w.StrandedLeases)
	}
	if w.StuckUnknown != 1 || w.StuckOperations["project_commit"] != 1 {
		t.Fatalf("stuck: %d %v", w.StuckUnknown, w.StuckOperations)
	}
	if w := Compute(context.Background(), nil, now); w.Complete {
		t.Fatal("no store must not claim completeness")
	}
}
