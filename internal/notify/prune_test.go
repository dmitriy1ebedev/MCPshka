package notify

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

// Intermediate stages are not events, DONE closes the start, and a full feed
// drops old closed/INFO events instead of refusing a new warning.
func TestReleaseStagesAndFullFeed(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, stage := range []string{"QUEUED", "SHIP", "BUILD", "DONE"} {
		if err := Release(db, "0.12.8", stage, "", now); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := Open(context.Background(), db)
	if len(events) != 1 || events[0].Key != "release/0.12.8/DONE" {
		t.Fatalf("%+v", events)
	}
	old := now.Add(-2 * keep)
	for i := 0; i < capacity; i++ {
		raw, _ := json.Marshal(Event{Key: "old/" + strconv.Itoa(i), Severity: "INFO", Count: 1, FirstAt: old, ObservedAt: old})
		if err := db.Put(prefix+"old"+strconv.Itoa(i)+".json", raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := Record(db, Event{Key: "fresh", Severity: "WARNING", Message: "m", Source: "s"}, false, now); err != nil {
		t.Fatalf("full feed refused a warning: %v", err)
	}
}
