package mission

import (
	"context"
	"strings"
	"testing"
)

func TestColdMissionDiscoversBeforePolicyAndAcceptance(t *testing.T) {
	f := controllerFixture(t)
	f.start(t)
	if len(f.calls) < 3 || f.calls[0] != "capabilities_bootstrap" || f.calls[1] != "tool_resolve" {
		t.Fatalf("cold mission skipped discovery: %v", f.calls)
	}
}

func TestMissionSchemaDriftBlocksBeforeAcceptance(t *testing.T) {
	f := controllerFixture(t)
	original := f.service.call
	f.service.call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name == "tool_resolve" {
			if args["expected_revision"] != strings.Repeat("a", 64) || args["compact"] != true {
				t.Fatal("resolve not pinned and compact", args)
			}
			return map[string]any{"code": "CLIENT_SCHEMA_STALE", "revision": strings.Repeat("b", 64)}, nil
		}
		return original(ctx, name, args)
	}
	out, err := f.service.Preflight(context.Background(), Plan{Path: f.root, Goal: "audit", Mode: "AUDIT_ONLY", Step: "P7"})
	if err != nil || out.State != "BLOCKED" || len(out.Blockers) == 0 || out.Blockers[0].NextSafeAction.Tool != "capabilities_bootstrap" {
		t.Fatal("schema drift did not block", out, err)
	}
	for _, call := range f.calls {
		if call == "acceptance_status" {
			t.Fatal("acceptance queried using stale contract")
		}
	}
}
