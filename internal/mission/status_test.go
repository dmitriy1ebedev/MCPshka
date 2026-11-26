package mission

import (
	"testing"
	"time"
)

func TestStalledHeartbeatNeverAuthorizesWriter(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		unit string
		age  time.Duration
		want string
	}{
		{"active", time.Hour, "degraded"}, {"inactive", time.Second, "degraded"}, {"active", time.Second, "ok"}, {"unknown", time.Second, "unknown"},
	} {
		r := Evaluate(now, []Observation{{Kind: "agent_task", ID: "task", Path: "/repo", Running: true, UnitState: tc.unit, EvidenceAt: now.Add(-tc.age)}}, nil, true)
		if r.State != tc.want || r.Items[0].NextSafeAction.Tool != "agent_task_status" || r.Items[0].NextSafeAction.Approval != "none" {
			t.Fatalf("%+v: %+v", tc, r)
		}
		if r.IndependentWorkAvailable == nil || *r.IndependentWorkAvailable {
			t.Fatal("busy project offered as free")
		}
	}
}

func TestMissingEvidenceAndIndependentWork(t *testing.T) {
	now := time.Now().UTC()
	for _, at := range []time.Time{{}, now.Add(time.Hour)} {
		r := Evaluate(now, []Observation{{EvidenceAt: at}}, nil, false)
		if r.State != "unknown" || r.Items[0].EvidenceAge != nil || r.IndependentWorkAvailable != nil {
			t.Fatal(r)
		}
	}
	reset := now.Add(time.Hour)
	r := Evaluate(now, []Observation{{Kind: "agent_task", EvidenceAt: now, BlockedBy: []string{"quota"}, ResetAt: reset}}, []string{"/free-worktree"}, true)
	if r.State != "degraded" || !*r.IndependentWorkAvailable || !r.Items[0].NextCheck.Equal(reset) {
		t.Fatal(r)
	}
}

func TestStoppedMissionCannotLookHealthyWithoutRecognizedError(t *testing.T) {
	now := time.Now().UTC()
	for _, phase := range []string{"failed", "interrupted", "stopped", "P8:stopped"} {
		report := Evaluate(now, []Observation{{Kind: "agent_task", ID: "task", Phase: phase, EvidenceAt: now}}, nil, true)
		if report.State != "degraded" || len(report.Items[0].BlockedBy) != 1 || report.Items[0].BlockedBy[0] != "worker" || report.Items[0].NextSafeAction.Tool != "agent_task_status" {
			t.Fatal(report)
		}
	}
}

func TestAbandonedRunsAreArchivedNotBlockers(t *testing.T) {
	now := time.Now().UTC()
	r := Evaluate(now, []Observation{
		{Kind: "roadmap_run", ID: "old", Phase: "stopped", EvidenceAt: now.Add(-5 * 24 * time.Hour)},
		{Kind: "roadmap_run", ID: "fresh", Phase: "stopped", EvidenceAt: now.Add(-time.Hour)},
		{Kind: "agent_task", ID: "long", Running: true, UnitState: "active", EvidenceAt: now.Add(-5 * 24 * time.Hour)},
		{Kind: "integration", ID: "stuck", EvidenceAt: now.Add(-5 * 24 * time.Hour)},
	}, nil, true)
	if r.Archived != 1 || len(r.Items) != 3 || r.Items[0].ID != "fresh" || r.Items[1].ID != "long" || r.Items[2].ID != "stuck" {
		t.Fatalf("stale stopped run must be archived, live and fresh kept: %+v", r)
	}
}
