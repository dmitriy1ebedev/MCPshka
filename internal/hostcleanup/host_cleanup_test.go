package hostcleanup

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/executor"
	"strings"
	"testing"
	"time"
)

func TestHostCleanupStepDeadlineIsPartial(t *testing.T) {
	for _, step := range []string{"storage", "cleanup"} {
		t.Run(step, func(t *testing.T) {
			called := false
			var saved []byte
			checkBudget := func(ctx context.Context, max time.Duration) {
				t.Helper()
				d, ok := ctx.Deadline()
				if !ok || time.Until(d) > max {
					t.Fatal("unbounded maintenance step")
				}
			}
			raw, err := runHostCleanupPass(context.Background(), func(ctx context.Context) error {
				checkBudget(ctx, 25*time.Second)
				if step == "storage" {
					return context.DeadlineExceeded
				}
				return nil
			}, func() bool { return true }, func(ctx context.Context) (executor.ReleaseCleanupPlan, error) {
				called = true
				checkBudget(ctx, 45*time.Second)
				p := executor.ReleaseCleanupPlan{Deleted: 2, DeletedBytes: 123}
				if step == "cleanup" {
					return p, context.DeadlineExceeded
				}
				return p, nil
			}, func(raw []byte) error { saved = append([]byte(nil), raw...); return nil })
			if err != nil {
				t.Fatal("budget failed unit:", err)
			}
			var got map[string]any
			wantState := "PARTIAL"
			if step == "storage" {
				wantState = "COMPLETE"
			}
			if json.Unmarshal(raw, &got) != nil || got["state"] != wantState || got["deleted"] != float64(2) || !called {
				t.Fatalf("lost continuation or progress: %s", raw)
			}
			if step == "storage" && (got["storage_state"] != "PARTIAL" || !strings.Contains(got["storage_reason"].(string), "storage")) {
				t.Fatalf("lost sampling status: %s", raw)
			}
			if step == "cleanup" && (got["next_run"] == "" || !strings.Contains(got["reason"].(string), step)) {
				t.Fatalf("lost continuation: %s", raw)
			}
			if string(saved) != string(raw) {
				t.Fatal("persisted report differs from worker result")
			}
		})
	}
}

func TestHostCleanupKeepsRealFailure(t *testing.T) {
	fault := errors.New("corrupt authority")
	_, err := runHostCleanupPass(context.Background(), func(context.Context) error { return nil }, func() bool { return true },
		func(context.Context) (executor.ReleaseCleanupPlan, error) {
			return executor.ReleaseCleanupPlan{}, fault
		}, func([]byte) error { return nil })
	if !errors.Is(err, fault) {
		t.Fatal("real failure hidden:", err)
	}
}
