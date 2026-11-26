package mission

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWorkflowCompletionAdvancesBoundMissionWithoutAITurn(t *testing.T) {
	f := controllerFixture(t)
	state := "running"
	original := f.service.call
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name == "workflow_status" {
			return map[string]any{"id": "approved-run", "root": f.root, "scope": "P7", "state": state, "fingerprint": strings.Repeat("b", 64), "job_id": "original-job"}, nil
		}
		return original(ctx, name, args)
	}
	f.accepted["P7"] = true
	in := StartInput{Plan: Plan{Path: f.root, Goal: "finish approved workflow", Mode: "CLOSE_STEP", Step: "P7", WorkflowID: "approved-run"}, IdempotencyKey: "bound"}
	s, err := f.service.Start(context.Background(), in)
	if err != nil || s.State == "DONE" || s.NextSafeAction.Tool != "workflow_status" {
		t.Fatal("job ACK became mission acceptance", s, err)
	}
	f.service = NewController(f.service.db, f.service.git, f.service.call)
	state = "done"
	if err := f.service.WorkflowCompleted("approved-run"); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.Status(context.Background(), s.ID)
	if err != nil || after.State != "DONE" || after.LastProven == nil {
		t.Fatal("post-step did not checkpoint accepted mission", after, err)
	}
	if err := f.service.WorkflowCompleted("approved-run"); err != nil {
		t.Fatal("duplicate completion failed", err)
	}
	in.IdempotencyKey = "replacement-mission"
	if replay, err := f.service.Start(context.Background(), in); err != nil || replay.ID != s.ID {
		t.Fatal("same workflow admitted a replacement instead of recovering the original", err)
	}
}

func TestBoundWorkflowFingerprintCannotChangeAfterHandoff(t *testing.T) {
	f := controllerFixture(t)
	fingerprint := strings.Repeat("b", 64)
	original := f.service.call
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name == "workflow_status" {
			return map[string]any{"id": "approved-run", "root": f.root, "scope": "P7", "state": "done", "fingerprint": fingerprint}, nil
		}
		return original(ctx, name, args)
	}
	s, err := f.service.Start(context.Background(), StartInput{Plan: Plan{Path: f.root, Goal: "pinned workflow", Mode: "CLOSE_STEP", Step: "P7", WorkflowID: "approved-run"}, IdempotencyKey: "bound"})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint = strings.Repeat("c", 64)
	f.accepted["P7"] = true
	after, err := f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil || after.State != "BLOCKED" || after.Workflow == nil || after.Workflow.Fingerprint != s.Workflow.Fingerprint {
		t.Fatal("active contract changed silently", after, err)
	}
}

func TestBoundWorkflowPinSurvivesUnavailableDiscovery(t *testing.T) {
	f := controllerFixture(t)
	fingerprint := strings.Repeat("b", 64)
	unavailable := false
	original := f.service.call
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if unavailable && name == "capabilities_bootstrap" {
			return nil, errors.New("catalog unavailable")
		}
		if name == "workflow_status" {
			return map[string]any{"id": "approved-run", "root": f.root, "scope": "P7", "state": "done", "fingerprint": fingerprint}, nil
		}
		return original(ctx, name, args)
	}
	s, err := f.service.Start(context.Background(), StartInput{Plan: Plan{Path: f.root, Goal: "pinned workflow", Mode: "CLOSE_STEP", Step: "P7", WorkflowID: "approved-run"}, IdempotencyKey: "bound"})
	if err != nil {
		t.Fatal(err)
	}
	unavailable = true
	after, err := f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil || after.State != "BLOCKED" || after.Workflow == nil || after.Workflow.Fingerprint != s.Workflow.Fingerprint {
		t.Fatal("temporary discovery failure erased original contract", after, err)
	}
}

func TestWorkflowBindingHoldsForeignScopeAndUnknownOutcome(t *testing.T) {
	for _, kind := range []string{"foreign_scope", "UNKNOWN", "done_without_acceptance"} {
		t.Run(kind, func(t *testing.T) {
			f := controllerFixture(t)
			original := f.service.call
			f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
				if name == "workflow_status" {
					scope, state := "P7", kind
					if kind == "foreign_scope" {
						scope = "P8"
						state = "done"
					}
					if kind == "done_without_acceptance" {
						state = "done"
					}
					return map[string]any{"id": "approved-run", "root": f.root, "scope": scope, "state": state, "fingerprint": strings.Repeat("b", 64)}, nil
				}
				return original(ctx, name, args)
			}
			s, err := f.service.Start(context.Background(), StartInput{Plan: Plan{Path: f.root, Goal: "scope", Mode: "CLOSE_STEP", Step: "P7", WorkflowID: "approved-run"}, IdempotencyKey: "bound"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.service.WorkflowCompleted("approved-run"); err != nil && !errors.Is(err, ErrAcceptancePending) {
				t.Fatal(err)
			}
			s, err = f.service.Status(context.Background(), s.ID)
			if err != nil || s.State == "DONE" || s.LastProven != nil {
				t.Fatal("unverified workflow completed mission", s, err)
			}
		})
	}
}
