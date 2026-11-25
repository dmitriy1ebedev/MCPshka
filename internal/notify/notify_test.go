package notify

import (
	"context"
	"encoding/json"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDurableDedupRecoveryQuietAndOrdering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 23, 0, 0, 0, time.UTC)
	if err := db.Put("notify/quiet-hours.json", []byte(`{"start_hour":22,"end_hour":7,"utc_offset_minutes":0}`)); err != nil {
		t.Fatal(err)
	}
	e := Event{Key: "one", Severity: "WARNING", Message: "first", Source: "test"}
	if err := Record(db, e, false, now); err != nil {
		t.Fatal(err)
	}
	e.Message = "duplicate must not replace first"
	e.Severity = "FAILED"
	if err := Record(db, e, false, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	db, err = statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := Open(context.Background(), db)
	if err != nil || len(events) != 1 || events[0].Count != 2 || events[0].Message != "first" || !events[0].Quiet || events[0].Severity != "WARNING" {
		t.Fatalf("%+v %v", events, err)
	}
	if err := Record(db, Event{Key: "two", Severity: "INFO"}, false, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	events, err = Open(context.Background(), db)
	if err != nil || events[0].Key != "two" {
		t.Fatalf("%+v %v", events, err)
	}
	if err := Record(db, e, true, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	events, err = Open(context.Background(), db)
	if err != nil || len(events) != 1 || events[0].Key != "two" {
		t.Fatalf("%+v %v", events, err)
	}
	for _, hour := range []int{7, 12, 21} {
		if (QuietHours{StartHour: 22, EndHour: 7}).Active(now.Add(time.Duration(hour-23) * time.Hour)) {
			t.Fatal("quiet outside window")
		}
	}
}
func TestReleaseAndUnverifiedBackupSources(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ctx := context.Background()
	for _, stage := range []string{"QUEUED", "FAILED", "DONE"} {
		if err := Release(db, "0.12.8", stage, "", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := Release(db, "0.12.9", "SHIP", "waiting for an active Face ID window of owner", now); err != nil {
		t.Fatal(err)
	}
	if err := ObserveBackups(ctx, db, now); err != nil {
		t.Fatal(err)
	}
	events, err := Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Key] = true
	}
	if seen["release/0.12.8/FAILED"] || !seen["release/0.12.9/BLOCKED"] || !seen["backup/freshness"] {
		t.Fatal(events)
	}
}

func TestBackupRecoveryClosesStaleEvent(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ctx := context.Background()
	if err := ObserveBackups(ctx, db, now); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"state": "verified", "created_at": now.Add(-time.Hour), "restore_verified_at": now.Add(-time.Minute), "file_id": "opaque", "restored_records": 1, "size_bytes": 123})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put("backups/"+strings.Repeat("a", 32)+".json", raw); err != nil {
		t.Fatal(err)
	}
	if err := ObserveBackups(ctx, db, now); err != nil {
		t.Fatal(err)
	}
	events, err := Open(ctx, db)
	if err != nil || len(events) != 0 {
		t.Fatalf("%+v %v", events, err)
	}
	if err := ObserveBackups(ctx, db, now.Add(27*time.Hour)); err != nil {
		t.Fatal(err)
	}
	events, err = Open(ctx, db)
	if err != nil || len(events) != 1 {
		t.Fatalf("%+v %v", events, err)
	}
}
