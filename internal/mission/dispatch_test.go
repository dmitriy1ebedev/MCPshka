package mission

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
)

func TestMissionRetainsOriginalDispatchKeyAfterCommitAndColdResume(t *testing.T) {
	f := controllerFixture(t)
	plan := Plan{Path: f.root, Goal: "finish P7", Mode: "CLOSE_STEP", Step: "P7", AgentsAllowed: true}
	first, err := f.service.Start(context.Background(), StartInput{Plan: plan, IdempotencyKey: "original-mission"})
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]any
	json.Unmarshal(first.NextSafeAction.Args, &original)
	cmd := exec.Command("git", "-C", f.root, "commit", "--allow-empty", "-qm", "work survived interruption")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v %s", err, raw)
	}
	f.service = NewController(f.service.db, f.service.git, f.service.call)
	resumed, err := f.service.Resume(context.Background(), ControlInput{ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	var next map[string]any
	json.Unmarshal(resumed.NextSafeAction.Args, &next)
	if next["idempotency_key"] != original["idempotency_key"] {
		t.Fatalf("cold resume created a replacement dispatch: %v != %v", next["idempotency_key"], original["idempotency_key"])
	}
	if resumed.ID != first.ID || resumed.State == "DONE" {
		t.Fatal("lost durable mission identity", resumed)
	}
}

func TestMissionResumeAdvancesAllProvenStepsWithoutAnotherTurn(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	f.accepted["P1"], f.accepted["P2"] = true, true
	s, err := f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil || s.State != "DONE" || s.Current != 2 || len(s.Checkpoints) != 2 {
		t.Fatal("proven next transition required another AI turn", s, err)
	}
}
