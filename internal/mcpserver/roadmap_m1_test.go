package mcpserver

import (
	"strings"
	"testing"

	"mcpshka/internal/agentguide"
	"mcpshka/internal/idempotency"
)

func TestRoadmapGoalRetryReturnsOriginalRun(t *testing.T) {
	store := idempotency.New(t.TempDir())
	calls := 0
	in := RoadmapRunInput{Goal: "repair arbitrary code", Path: "/root/repos/example", IdempotencyKey: "same"}
	start := func() (map[string]any, error) {
		calls++
		return map[string]any{"id": "original", "state": "STARTED"}, nil
	}
	for i := 0; i < 2; i++ {
		out, err := runIdempotent(store, in.IdempotencyKey, "roadmap_run", in, start)
		if err != nil || out["id"] != "original" {
			t.Fatalf("%v %v", out, err)
		}
	}
	in.Goal = "different task"
	if _, err := runIdempotent(store, in.IdempotencyKey, "roadmap_run", in, start); err == nil {
		t.Fatal("key reused for different goal")
	}
	if calls != 1 {
		t.Fatal("duplicate run", calls)
	}
	if !strings.HasPrefix(capabilityInstructions, agentguide.DevelopmentInstructions) {
		t.Fatal("bootstrap lacks first instruction")
	}
}
