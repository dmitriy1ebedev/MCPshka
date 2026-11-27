package roadmaprun

import (
	"testing"
	"time"
)

func TestOlderStoppedRunsAreSuperseded(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 5, 0, 0, 0, time.UTC)
	runs := []Journal{
		{ID: "c", Path: "/r/lane", State: "stopped", Step: "blocked", NextAction: "Face ID", StartedAt: t0.Add(2 * time.Hour)},
		{ID: "b", Path: "/r/lane", State: "stopped", Step: "reconciliation_required", NextAction: "reconcile", StartedAt: t0.Add(time.Hour)},
		{ID: "a", Path: "/r/lane", State: "stopped", Step: "blocked", NextAction: "retry", StartedAt: t0},
		{ID: "x", Path: "/r/diag", State: "stopped", Step: "blocked", NextAction: "retry", StartedAt: t0},
	}
	markSuperseded(runs)
	if runs[0].SupersededBy != "" || runs[0].NextAction != "Face ID" {
		t.Fatalf("newest run changed: %+v", runs[0])
	}
	if runs[1].SupersededBy != "c" || runs[1].NextAction != "reconcile" {
		t.Fatalf("reconciliation hint lost: %+v", runs[1])
	}
	if runs[2].SupersededBy != "c" || runs[2].NextAction != "none: superseded by c" {
		t.Fatalf("old run still open: %+v", runs[2])
	}
	if runs[3].SupersededBy != "" {
		t.Fatalf("other project touched: %+v", runs[3])
	}
}
