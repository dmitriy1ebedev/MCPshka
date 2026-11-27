package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// DIAG ACTIVE-01 / MCP-125: terminal launches are settled once so later
// sweeps skip them instead of re-proving history until the deadline.
func TestIntegrationMaintenanceSettlesOnlyTerminalClaims(t *testing.T) {
	b, id := integrationMaintenanceTestFixture()
	if settled, err := maintainIntegrationWorkerSettled(context.Background(), id, b); settled || !errors.Is(err, ErrHold) {
		t.Fatalf("live worker settled=%v err=%v", settled, err)
	}
	b.runtime.Claim.State = "RELEASED"
	b.retireErr = errors.New("retire slow")
	if settled, _ := maintainIntegrationWorkerSettled(context.Background(), id, b); settled {
		t.Fatal("failed retire settled the launch")
	}
	b.retireErr = nil
	if settled, err := maintainIntegrationWorkerSettled(context.Background(), id, b); !settled || err != nil {
		t.Fatalf("retired launch not settled: %v %v", settled, err)
	}
	b.runtime.Claim.State = "FAILED"
	if settled, err := maintainIntegrationWorkerSettled(context.Background(), id, b); !settled || err != nil {
		t.Fatalf("failed claim not settled: %v %v", settled, err)
	}
}

func TestMaintenanceLoopReportsEverySweepOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var outcomes []error
	calls := 0
	runMaintenanceLoop(ctx, time.Millisecond, time.Second, func(c context.Context, after string) (string, error) {
		calls++
		if _, ok := c.Deadline(); !ok {
			t.Error("sweep without deadline")
		}
		if calls == 3 {
			cancel()
			return after, c.Err()
		}
		if calls == 1 {
			return "k", ErrHold
		}
		return "", nil
	}, func(err error) { outcomes = append(outcomes, err) })
	if len(outcomes) != 2 || !errors.Is(outcomes[0], ErrHold) || outcomes[1] != nil {
		t.Fatalf("outcomes %v", outcomes)
	}
}
