package runoutcome

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/notify"
	"mcpshka/internal/statestore"
)

func TestOwnerEventRequiresTwoNoEffectRuns(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	result := Resolve(0, true, "lock_held", "observe running job")
	idle := Resolve(0, false, "", "")
	done := Resolve(2, true, "", "")
	if result.State != "no_effect" || result.Effect || idle.State != "idle" || done.State != "completed" || !done.Effect {
		t.Fatal(result, idle, done)
	}
	for _, sample := range []struct {
		id     string
		result Result
		want   int
	}{
		{"run-1", result, 0},
		{"run-1", result, 0},
		{"run-2", result, 1},
		{"run-3", idle, 0},
		{"run-4", result, 0},
		{"run-5", done, 0},
	} {
		if err := Record(db, "archive-test", sample.id, sample.result, time.Now()); err != nil {
			t.Fatal(err)
		}
		events, err := notify.Open(context.Background(), db)
		if err != nil || len(events) != sample.want {
			t.Fatalf("%s: %+v %v", sample.id, events, err)
		}
	}
}
