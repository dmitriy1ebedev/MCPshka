package mission

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"mcpshka/internal/evidence"
)

func TestReleaseMissionCannotFinishAtBuildPass(t *testing.T) {
	f := controllerFixture(t)
	f.accepted["P1"] = true
	s, err := f.service.Start(context.Background(), StartInput{Plan: Plan{Path: f.root, Goal: "install and accept", Mode: "REPAIR_AND_RELEASE", Step: "P1"}, IdempotencyKey: "release"})
	if err != nil {
		t.Fatal(err)
	}
	s, err = f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil || s.State == "DONE" {
		t.Fatalf("build PASS was treated as full release: %s %v", s.State, err)
	}
}
func TestReleaseMissionAcceptsCompletePinnedGateSet(t *testing.T) {
	f := controllerFixture(t)
	original := f.service.call
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name != "acceptance_status" {
			return original(ctx, name, args)
		}
		inputs := evidence.Inputs{Head: f.head, Tree: f.tree, Version: "0.8.2"}
		a := Acceptance{Done: true, Inputs: inputs}
		for _, g := range []string{"engineering", "integration", "release", "deploy", "acceptance", "cleanup"} {
			a.Gates = append(a.Gates, evidence.GateState{Gate: g, State: "PASS", Record: &evidence.Record{Project: f.root, Gate: g, Result: "PASS", Inputs: inputs}})
		}
		raw, _ := json.Marshal(a)
		var out map[string]any
		json.Unmarshal(raw, &out)
		return out, nil
	}
	s, err := f.service.Start(context.Background(), StartInput{Plan: Plan{Path: f.root, Goal: "install and accept", Version: "0.8.2", Mode: "REPAIR_AND_RELEASE", Step: "P1"}, IdempotencyKey: "release"})
	if err != nil {
		t.Fatal(err)
	}
	s, err = f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil || s.State != "DONE" {
		t.Fatalf("complete release evidence cannot be accepted: %s %v", s.State, err)
	}
}
func TestMissionDeadlineIsDurableAndStopsDispatch(t *testing.T) {
	f := controllerFixture(t)
	raw, _ := json.Marshal(map[string]any{"path": f.root, "goal": "bounded mission", "mode": "CLOSE_STEP", "step": "P1", "agents_allowed": true, "deadline": time.Now().Add(-time.Minute)})
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	s, err := f.service.Start(context.Background(), StartInput{Plan: p, IdempotencyKey: "deadline"})
	if err != nil || s.State != "EXPIRED" || s.NextSafeAction.Tool == "agent_task" {
		t.Fatalf("expired mission dispatches work: %s %s %v", s.State, s.NextSafeAction.Tool, err)
	}
	cold := NewController(f.service.db, f.service.git, f.service.call)
	s, err = cold.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil || s.State != "EXPIRED" {
		t.Fatal("restart renewed deadline", s.State, err)
	}
}
