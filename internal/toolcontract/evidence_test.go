package toolcontract

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestObserveProbeBudgets(t *testing.T) {
	for _, tc := range []struct {
		probe  string
		budget time.Duration
	}{
		{"executor_admin_stale_preview", 40 * time.Second},
		{"state_status", 25 * time.Second},
	} {
		t.Run(tc.probe, func(t *testing.T) {
			start := time.Now()
			ev, err := Observe(context.Background(), tc.probe, nil, func(ctx context.Context, _ map[string]any) (any, error) {
				deadline, ok := ctx.Deadline()
				if !ok || deadline.Before(start.Add(tc.budget)) || deadline.After(time.Now().Add(2*tc.budget)) {
					t.Fatalf("deadline %v does not match budget %v", deadline, tc.budget)
				}
				return map[string]any{"state": "ready"}, nil
			})
			if err != nil || ev.State != "observed" || ev.SHA256 == "" {
				t.Fatalf("evidence = %+v, err = %v", ev, err)
			}
		})
	}
}

func TestObserveStalePreviewRespectsParentAndFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	ev, err := Observe(ctx, "executor_admin_stale_preview", nil, func(child context.Context, _ map[string]any) (any, error) {
		got, ok := child.Deadline()
		if !ok || !got.Equal(deadline) {
			t.Fatal("parent deadline extended")
		}
		cancel()
		return nil, child.Err()
	})
	if !errors.Is(err, context.Canceled) || ev.State != "unknown" || ev.SHA256 != "" {
		t.Fatalf("failed probe accepted: %+v, err = %v", ev, err)
	}
}

func TestMemoryArchiveProbesExactRunAfterMutation(t *testing.T) {
	in := map[string]any{"source": "codex", "claim_key": "private-claim"}
	for _, field := range []string{"run", "receipt"} {
		out := map[string]any{field: map[string]any{"run_id": "archive-1"}}
		before, args, err := ProbeArguments("memory_archive", "memory_archive_status", in, nil, true)
		if err != nil || before != "state_status" || len(args) != 0 {
			t.Fatal(before, args, err)
		}
		after, args, err := ProbeArguments("memory_archive", "memory_archive_status", in, out, false)
		if err != nil || after != "memory_archive_status" || len(args) != 2 || args["source"] != "codex" || args["run_id"] != "archive-1" {
			t.Fatal(after, args, err)
		}
	}
}
