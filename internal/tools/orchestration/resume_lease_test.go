package orchestration

import (
	"context"
	"mcpshka/internal/agents"
	"testing"
	"time"
)

func TestResumeReleasesOnlyProvenDeadTaskLease(t *testing.T) {
	old := resumeUnitsEnded
	defer func() { resumeUnitsEnded = old }()
	task := agents.Task{ID: "0123456789abcdef", Path: "/root/repos/demo", State: "failed", Lease: &agents.TaskLeaseCheckpoint{State: "unknown"}}
	for _, gone := range []bool{false, true} {
		lease := *task.Lease
		sample := task
		sample.Lease = &lease
		held, releases := true, 0
		resumeUnitsEnded = func(string) bool { return gone }
		d := AgentDeps{LeaseHeldBy: func(string, string) bool { return held }, ReleaseTaskLease: func(_ context.Context, session, owner string, got agents.Task) string {
			if session != "face-id" || owner != "owner" || got.ID != task.ID {
				t.Fatal(session, owner, got.ID)
			}
			releases++
			held = false
			return "released"
		}}
		err := recoverResumeLease(context.Background(), sample, "face-id", "owner", d)
		if (err == nil) != gone || (releases == 1) != gone {
			t.Fatal(gone, releases, err)
		}
	}
	resumeUnitsEnded = func(string) bool { return true }
	d := AgentDeps{LeaseHeldBy: func(string, string) bool { return true }, ReleaseTaskLease: func(context.Context, string, string, agents.Task) string { return "kept: jobs active" }}
	if err := recoverResumeLease(context.Background(), task, "face-id", "owner", d); err == nil {
		t.Fatal("successor started while lease remained held")
	}
	legacy := task
	legacy.Lease = nil
	if err := recoverResumeLease(context.Background(), legacy, "face-id", "owner", d); err == nil {
		t.Fatal("legacy record hid a held lease")
	}
	task.State, task.ResumeAt = "retry_scheduled", time.Now().Add(time.Hour)
	d.LeaseHeldBy = func(string, string) bool { return false }
	if err := recoverResumeLease(context.Background(), task, "face-id", "owner", d); err != nil {
		t.Fatal("dead runner timer blocked explicit resume", err)
	}
}
