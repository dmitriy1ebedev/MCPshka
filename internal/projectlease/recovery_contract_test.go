package projectlease

import (
	"encoding/json"
	"mcpshka/internal/failure"
	"mcpshka/internal/jobs"
	"strings"
	"testing"
)

func TestRecoveryPreviewNamesMissingEvidence(t *testing.T) {
	for _, reason := range []string{"source must be root-owned and sealed 0500", "live process references: HOLD", "deployment authority unavailable", "source tree is dirty; preserve work"} {
		p := (RootSealedRecoveryPreview{Reason: reason}).WithState()
		raw, _ := json.Marshal(p)
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		if fields["next_action"] == nil || fields["blockers"] == nil || fields["disposition"] != "blocker" {
			t.Errorf("missing machine-readable recovery contract: %s", raw)
		}
	}
}

func TestRecoveryQuiescenceKeepsFailedAndUnknownJobsFenced(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	key := "jobs/fixture/metadata.json"
	for _, state := range []jobs.State{jobs.Running, jobs.Queued, jobs.Unknown, jobs.Failed} {
		raw, _ := json.Marshal(jobs.Snapshot{ID: "fixture", Workdir: root, State: state})
		if err := s.database.Put(key, raw); err != nil {
			t.Fatal(err)
		}
		err := s.recoveryQuiescent(root)
		details := failure.Details(err)
		if details == nil || details.Code != "project_jobs_unverified" {
			t.Fatal("unverified job admitted", state, err)
		}
	}
	identity, err := jobs.CurrentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	rawNative, _ := json.Marshal(jobs.Snapshot{ID: "fixture", Workdir: root, State: jobs.Failed, PID: 2147483000, StartTicks: 1, BootID: identity.BootID})
	if err := s.database.Put(key, rawNative); err != nil {
		t.Fatal(err)
	}
	if err := s.recoveryQuiescent(root); err != nil {
		t.Fatal("terminal native job waited forever for a flag it never writes", err)
	}
	raw, _ := json.Marshal(jobs.Snapshot{ID: "fixture", Workdir: root, State: jobs.Failed, ResourcesReleased: true})
	if err := s.database.Put(key, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.recoveryQuiescent(root); err != nil {
		t.Fatal("failed test with verified released resources blocked recovery", err)
	}
	if err := s.database.Put("deploys/unknown.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := s.recoveryQuiescent(root); err == nil {
		t.Fatal("unknown deployment admitted")
	}
}

func TestRecoveryOwnerUsesPIDStartAndBoot(t *testing.T) {
	identity, err := jobs.CurrentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if gone, err := identity.Gone(); err != nil || gone {
		t.Fatal("live owner considered dead", err)
	}
	identity.StartTicks++
	if gone, err := identity.Gone(); err != nil || !gone {
		t.Fatal("reused PID mistaken for former owner", err)
	}
	identity.StartTicks = 0
	if gone, err := identity.Gone(); err == nil || gone {
		t.Fatal("legacy incomplete identity treated as death")
	}
}

func TestRecoveryBoundLiveOwnerCannotTransfer(t *testing.T) {
	identity, err := jobs.CurrentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	r := record{ProjectRoot: "/fixture", OwnerPID: identity.PID, OwnerStartTicks: identity.StartTicks, OwnerBootID: identity.BootID}
	if err := recoveryBoundOwner(r); err == nil {
		t.Fatal("live bound owner offered for transfer")
	}
	r.OwnerStartTicks++
	if err := recoveryBoundOwner(r); err != nil {
		t.Fatal("PID reuse blocked gone incarnation", err)
	}
	r.OwnerStartTicks = 0
	if err := recoveryBoundOwner(r); err == nil {
		t.Fatal("partial legacy binding proved death")
	}
}

func TestLegacyRecoveryPlanKeepsEmptySnapshotEncoding(t *testing.T) {
	raw, err := json.Marshal(RootSealedRecovery{})
	if err != nil || strings.Contains(string(raw), "WorktreeSHA256") {
		t.Fatal("empty optional snapshot changed legacy gate encoding", err)
	}
}
