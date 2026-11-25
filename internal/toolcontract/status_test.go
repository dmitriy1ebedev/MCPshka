package toolcontract

import (
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"testing"
)

func TestLeaseRecoveryStatusNeverInfersOwnerDeathFromEmptyJobs(t *testing.T) {
	held := projectlease.Status{Held: true, ProjectRoot: "/repo", Owner: "legacy"}
	out := LeaseRecoveryStatus(held, jobs.ProjectActivity{})
	if out.Reclaimable || len(out.RecoveryBlockers) < 4 {
		t.Fatalf("unverified owner became reclaimable: %+v", out)
	}
	out = LeaseRecoveryStatus(held, jobs.ProjectActivity{Count: 1, UnknownCount: 1})
	if out.Reclaimable || out.RecoveryBlockers[len(out.RecoveryBlockers)-1] != "project_jobs_pending" {
		t.Fatalf("unknown job was ignored: %+v", out)
	}
	out = LeaseRecoveryStatus(projectlease.Status{ProjectRoot: "/repo"}, jobs.ProjectActivity{})
	if len(out.RecoveryBlockers) != 0 {
		t.Fatalf("absent lease needs no recovery: %+v", out)
	}
}

func TestLeaseRecoveryStatusDoesNotHideExistingProcessBinding(t *testing.T) {
	out := LeaseRecoveryStatus(projectlease.Status{Held: true, OwnerPID: 123, OwnerStartTicks: 456, OwnerBootID: "boot"}, jobs.ProjectActivity{})
	for _, reason := range out.RecoveryBlockers {
		if reason == "pid_start_time_binding_missing" {
			t.Fatal("existing binding reported missing")
		}
	}
	if out.Reclaimable {
		t.Fatal("recorded binding alone proved death")
	}
}
