package jobs

import (
	"strings"
	"testing"
	"time"

	"mcpshka/internal/policy"
)

func TestRecoveryResourcesGoneLegacyJobBeforeBoot(t *testing.T) {
	boot := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	old := bootStartedAt
	bootStartedAt = func() (time.Time, error) { return boot, nil }
	t.Cleanup(func() { bootStartedAt = old })

	legacy := Snapshot{ID: "muyx7q88-x", State: Done, CommandClass: policy.ClassMutating, PID: 2824615,
		StartedAt: time.Date(2026, 10, 8, 2, 31, 58, 0, time.UTC), FinishedAt: time.Date(2026, 10, 8, 2, 32, 6, 0, time.UTC)}
	gone, err := RecoveryResourcesGone(legacy)
	if err != nil || !gone {
		t.Fatalf("legacy job finished before boot must be gone: %v %v", gone, err)
	}

	current := legacy
	current.StartedAt, current.FinishedAt = boot.Add(time.Hour), boot.Add(2*time.Hour)
	gone, err = RecoveryResourcesGone(current)
	if gone || err == nil || !strings.Contains(err.Error(), "muyx7q88-x") {
		t.Fatalf("unbound job of the current boot must HOLD naming the job: %v %v", gone, err)
	}

	edge := legacy
	edge.StartedAt, edge.FinishedAt = boot.Add(-2*time.Minute), boot.Add(-time.Minute)
	if gone, _ := RecoveryResourcesGone(edge); gone {
		t.Fatal("job finishing within the clock margin of boot must not be proven gone")
	}

	running := legacy
	running.State = Running
	if gone, err := RecoveryResourcesGone(running); gone || err != nil {
		t.Fatalf("running job is never gone: %v %v", gone, err)
	}
}
