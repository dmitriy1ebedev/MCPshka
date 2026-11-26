package mission

import (
	"context"
	"mcpshka/internal/failure"
	"strings"
	"testing"
	"time"
)

func TestMissionControlRejectsForeignWorkflowBeforeAnyEffect(t *testing.T) {
	for _, field := range []string{"root", "scope", "fingerprint"} {
		t.Run(field, func(t *testing.T) {
			f := controllerFixture(t)
			original := f.service.call
			foreign := field != "fingerprint"
			f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
				if name == "workflow_status" {
					root, scope, fp := f.root, "P7", strings.Repeat("b", 64)
					if foreign {
						switch field {
						case "root":
							root = f.root + "/foreign"
						case "scope":
							scope = "P8"
						case "fingerprint":
							fp = strings.Repeat("c", 64)
						}
					}
					return map[string]any{"id": "approved-run", "root": root, "scope": scope, "state": "pending", "fingerprint": fp}, nil
				}
				return original(ctx, name, args)
			}
			effects := 0
			f.service.WorkflowControl = func(string, string, string, *time.Time) error { effects++; return nil }
			in := StartInput{Plan: Plan{Path: f.root, Goal: "only P7 in this repository", Mode: "CLOSE_STEP", Step: "P7", WorkflowID: "approved-run"}, IdempotencyKey: "bound"}
			s, err := f.service.Start(context.Background(), in)
			if field != "fingerprint" {
				if err == nil || !failure.IsNoEffect(err) || effects != 0 {
					t.Fatal("foreign workflow mutated before identity validation", field, effects, err)
				}
				return
			}
			if err != nil || effects != 1 {
				t.Fatal("valid initial binding failed", effects, err)
			}
			foreign = true
			if _, err := f.service.Pause(ControlInput{ID: s.ID}); err == nil || !failure.IsNoEffect(err) || effects != 1 {
				t.Fatal("changed workflow was controlled through a stale mission", effects, err)
			}
		})
	}
}

func TestMissionControlAuditOnlyNeverTouchesTheExecutor(t *testing.T) {
	f := controllerFixture(t)
	original := f.service.call
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name == "workflow_status" {
			return map[string]any{"id": "approved-run", "root": f.root, "scope": "P7", "state": "pending", "fingerprint": strings.Repeat("b", 64)}, nil
		}
		return original(ctx, name, args)
	}
	effects := 0
	f.service.WorkflowControl = func(string, string, string, *time.Time) error { effects++; return nil }
	deadline := time.Now().Add(time.Hour)
	s, err := f.service.Start(context.Background(), StartInput{Plan: Plan{Path: f.root, Goal: "observe only", Mode: "AUDIT_ONLY", Step: "P7", WorkflowID: "approved-run", Deadline: &deadline}, IdempotencyKey: "audit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Pause(ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Tick(context.Background(), ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Resume(context.Background(), ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Cancel(ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatal("AUDIT_ONLY changed another workflow", effects)
	}
}
