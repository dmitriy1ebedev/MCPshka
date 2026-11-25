package mission

import (
	"context"
	"testing"
)

func TestMissionMaintenanceContinuesProvenTaskWithoutChatTurn(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	f.accepted["P1"], f.accepted["P2"] = true, true
	cold := NewController(f.service.db, f.service.git, f.service.call)
	if err := cold.Maintain(); err != nil {
		t.Fatal(err)
	}
	stored, _, err := cold.load(s.ID)
	if err != nil || stored.State != "DONE" || stored.Current != 2 {
		t.Fatal("maintenance failed to checkpoint acceptance", stored, err)
	}
	before := len(stored.History)
	if err := cold.Maintain(); err != nil {
		t.Fatal(err)
	}
	stored, _, _ = cold.load(s.ID)
	if len(stored.History) != before {
		t.Fatal("terminal maintenance churned history")
	}
	for _, name := range f.calls {
		if name == "agent_task" || name == "roadmap_run" {
			t.Fatal("maintenance launched AI")
		}
	}
}
func TestMissionMaintenancePreservesPause(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	if _, err := f.service.Pause(ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	f.accepted["P1"], f.accepted["P2"] = true, true
	if err := f.service.Maintain(); err != nil {
		t.Fatal(err)
	}
	s, err := f.service.Status(context.Background(), s.ID)
	if err != nil || s.State != "PAUSED" || len(s.Checkpoints) != 0 {
		t.Fatal(s, err)
	}
}

// A BLOCKED mission's full preflight is not re-run on every 10 s pass: the
// 08.11 prod loop timed out every pass and degraded mission readiness itself.
func TestMaintenanceSpacesBlockedRechecks(t *testing.T) {
	f := controllerFixture(t)
	original := f.service.call
	preflights := 0
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name == "capability_preflight" {
			preflights++
			return map[string]any{"verdict": "blocked", "blocker": "test refusal"}, nil
		}
		return original(ctx, name, args)
	}
	s, err := f.service.Start(context.Background(), StartInput{Plan: Plan{Path: f.root, Goal: "S1 blocked", Mode: "CLOSE_STEP", Step: "S1"}, IdempotencyKey: "blocked"})
	if err != nil || s.State != "BLOCKED" {
		t.Fatalf("fixture mission not blocked: %+v %v", s, err)
	}
	if err := f.service.Maintain(); err != nil {
		t.Fatal(err)
	}
	first := preflights
	if err := f.service.Maintain(); err != nil {
		t.Fatal(err)
	}
	if preflights != first {
		t.Fatalf("blocked mission re-checked within %v: %d → %d preflights", reconcileEvery, first, preflights)
	}
	old := reconcileEvery
	reconcileEvery = 0
	t.Cleanup(func() { reconcileEvery = old })
	if err := f.service.Maintain(); err != nil {
		t.Fatal(err)
	}
	if preflights == first {
		t.Fatal("blocked mission never re-checked after the spacing window")
	}
}
