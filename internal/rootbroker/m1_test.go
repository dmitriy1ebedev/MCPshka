package rootbroker

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestRoadmapGoalContract(t *testing.T) {
	goal := base64.RawURLEncoding.EncodeToString([]byte("fix arbitrary task\nwith rules"))
	args := []string{"roadmap-run", strings.Repeat("a", 32), "owner", "/root/repos/example", "1", "8", goal, "agents:codex"}
	if err := validateRoadmapRun(args); err != nil {
		t.Fatal(err)
	}
	got := roadmapRunServiceArgs(args, "example-run")
	if got[len(got)-2] != goal || got[len(got)-1] != "agents:codex" {
		t.Fatal("goal not passed to runner")
	}
	for _, bad := range []string{"!invalid", base64.RawURLEncoding.EncodeToString([]byte("\x00")), base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 3073)))} {
		args[6] = bad
		if validateRoadmapRun(args) == nil {
			t.Fatal("invalid goal accepted")
		}
	}
}

func TestFailedPreparationIncludesOwnBoundedReason(t *testing.T) {
	own := "mcpshka-integration-admin-" + strings.Repeat("a", 32)
	state := failedPreparation(modulePrepareState{Unit: own}, "1")
	called := 0
	read := func(_ context.Context, unit string) (string, error) {
		called++
		if unit != own {
			t.Fatal("foreign unit", unit)
		}
		return "executor HOLD: admission or fencing unverified\nlease_token=private", nil
	}
	got := preparationLog(context.Background(), state, read)
	if got.State != "FAILED" || !strings.Contains(got.LogExcerpt, "admission or fencing unverified") || strings.Contains(got.LogExcerpt, "private") || !strings.Contains(got.NextAction, "project_context") || !strings.Contains(got.NextAction, "attempt 2") {
		t.Fatalf("%+v", got)
	}
	_ = preparationLog(context.Background(), modulePrepareState{State: "PREPARING"}, read)
	if called != 1 {
		t.Fatal("read a running/foreign unit")
	}
	got = preparationLog(context.Background(), state, func(context.Context, string) (string, error) { return "", errors.New("unavailable") })
	if !strings.Contains(got.LogError, "UNVERIFIED") {
		t.Fatal(got)
	}
}
