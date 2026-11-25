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
