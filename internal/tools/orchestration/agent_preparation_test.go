package orchestration

import (
	"errors"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/failure"
	"mcpshka/internal/idempotency"
	"testing"
)

func TestPreparedCircuitRollbackRetriesSameKey(t *testing.T) {
	s := idempotency.New(t.TempDir())
	calls, clean := 0, 0
	run := func() (map[string]any, error) {
		return dispatchPrepared(func() (map[string]any, error) {
			calls++
			if calls == 1 {
				return nil, brokerclient.ErrCircuitOpen
			}
			return map[string]any{"id": "task"}, nil
		}, func() error { clean++; return nil })
	}
	if _, err := idempotency.Run(s, "same", "agent_task", "input", run); !failure.IsNoEffect(err) {
		t.Fatal(err)
	}
	out, err := idempotency.Run(s, "same", "agent_task", "input", run)
	if err != nil || out["id"] != "task" || calls != 2 || clean != 1 {
		t.Fatal(out, err, calls, clean)
	}
}

func TestPreparedUnknownAndFailedRollbackStayFenced(t *testing.T) {
	for _, err := range []error{errors.New("lost reply"), brokerclient.ErrBusy} {
		_, got := dispatchPrepared(func() (map[string]any, error) { return nil, err }, func() error {
			if !errors.Is(err, brokerclient.ErrBusy) {
				t.Fatal("unknown rolled back")
			}
			return errors.New("dirty worktree")
		})
		if got == nil || failure.IsNoEffect(got) {
			t.Fatal(got)
		}
	}
}
