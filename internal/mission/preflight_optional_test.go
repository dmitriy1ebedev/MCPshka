package mission

import (
	"context"
	"testing"
)

// Optional resource pressure is a scheduling signal, not proof that the
// executor or broker is unavailable. Required-dependency degradation and
// missing evidence still fail closed.
func TestPreflightOptionalPressureVsRequiredFailure(t *testing.T) {
	f := controllerFixture(t)
	original := f.service.call
	requiredState := "ok"
	optionalState := "degraded"
	includeEvidence := true
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name != "capability_preflight" {
			return original(ctx, name, args)
		}
		response := map[string]any{"verdict": "degraded"}
		if includeEvidence {
			response["capabilities"] = []any{map[string]any{
				"state": "degraded",
				"dependencies": []any{
					map[string]any{"name": "executor", "state": requiredState, "required": true},
					map[string]any{"name": "resources", "state": optionalState, "required": false},
				},
			}}
		}
		return response, nil
	}
	plan := Plan{Path: f.root, Goal: "S1 resource safety", Mode: "CLOSE_STEP", Step: "S1"}
	preflight, err := f.service.Preflight(context.Background(), plan)
	if err != nil || preflight.State != "READY" || preflight.Steps[0].CapabilityVerdict != "degraded" {
		t.Fatalf("optional resource warning blocked safe preparation: %+v %v", preflight, err)
	}

	requiredState = "degraded"
	preflight, err = f.service.Preflight(context.Background(), plan)
	if err != nil || preflight.State != "BLOCKED" {
		t.Fatalf("required executor degradation was ignored: %+v %v", preflight, err)
	}

	requiredState = "ok"
	optionalState = "unknown"
	preflight, err = f.service.Preflight(context.Background(), plan)
	if err != nil || preflight.State != "BLOCKED" {
		t.Fatalf("unknown optional observation was treated as pass: %+v %v", preflight, err)
	}

	optionalState = "degraded"
	includeEvidence = false
	preflight, err = f.service.Preflight(context.Background(), plan)
	if err != nil || preflight.State != "BLOCKED" {
		t.Fatalf("missing dependency evidence was treated as pass: %+v %v", preflight, err)
	}
}

// 08.11 prod shape: the DIAG all-tiers mission stayed BLOCKED on every step
// because the verdict was "preflight" (stale optional resource sample, foreign
// stale runs degrading recovery) and read-only tools are only "registered".
// None of that is a refusal; a blocked verdict still is.
func TestPreflightStaleSampleAndRegisteredToolsDoNotBlock(t *testing.T) {
	f := controllerFixture(t)
	original := f.service.call
	verdict := "preflight"
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name != "capability_preflight" {
			return original(ctx, name, args)
		}
		return map[string]any{"verdict": verdict, "capabilities": []any{
			map[string]any{"state": "preflight", "dependencies": []any{
				map[string]any{"name": "executor", "state": "ok", "required": true},
				map[string]any{"name": "broker", "state": "ok", "required": true},
				map[string]any{"name": "resources", "state": "stale", "required": false},
			}},
			map[string]any{"state": "registered", "dependencies": []any{}},
		}}, nil
	}
	plan := Plan{Path: f.root, Goal: "S1 all tiers", Mode: "CLOSE_STEP", Step: "S1"}
	if p, err := f.service.Preflight(context.Background(), plan); err != nil || p.State != "READY" {
		t.Fatalf("advisory preflight blocked the mission: %+v %v", p, err)
	}
	verdict = "blocked"
	if p, err := f.service.Preflight(context.Background(), plan); err != nil || p.State != "BLOCKED" {
		t.Fatalf("blocked verdict ignored: %+v %v", p, err)
	}
}

// MCP-166: Lane 1.3.0 stayed BLOCKED on every step because integration_ship's
// optional "providers" source was unknown (no agent ran). A mission without
// agents must proceed; an unknown REQUIRED providers source still blocks.
func TestPreflightUnknownOptionalProvidersDoesNotBlock(t *testing.T) {
	f := controllerFixture(t)
	original := f.service.call
	required := false
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name != "capability_preflight" {
			return original(ctx, name, args)
		}
		return map[string]any{"verdict": "preflight", "capabilities": []any{
			map[string]any{"state": "preflight", "dependencies": []any{
				map[string]any{"name": "broker", "state": "ok", "required": true},
				map[string]any{"name": "providers", "state": "unknown", "required": required},
			}},
		}}, nil
	}
	plan := Plan{Path: f.root, Goal: "L1 no agents", Mode: "CLOSE_STEP", Step: "L1"}
	if p, err := f.service.Preflight(context.Background(), plan); err != nil || p.State != "READY" {
		t.Fatalf("optional unknown providers blocked the mission: %+v %v", p, err)
	}
	required = true
	if p, err := f.service.Preflight(context.Background(), plan); err != nil || p.State != "BLOCKED" {
		t.Fatalf("required unknown providers ignored: %+v %v", p, err)
	}
}
