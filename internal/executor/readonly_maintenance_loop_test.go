package executor

import (
	"context"
	"testing"
	"time"
)

func TestReadonlyMaintenanceShutdownDrainsExactSweep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	calls := 0
	go func() {
		defer close(done)
		runReadonlyMaintenance(ctx, time.Millisecond, func(s context.Context, cursor string) (string, error) {
			calls++
			entered <- s
			<-release
			return "observed", nil
		}, nil)
	}()
	var sweep context.Context
	select {
	case sweep = <-entered:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not start")
	}
	if deadline, ok := sweep.Deadline(); !ok || time.Until(deadline) > 11*time.Second {
		t.Fatal("sweep is unbounded")
	}
	cancel()
	select {
	case <-sweep.Done():
	case <-time.After(time.Second):
		t.Fatal("sweep context not cancelled")
	}
	select {
	case <-done:
		t.Fatal("shutdown returned while sweep still running")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not drain")
	}
	if calls != 1 {
		t.Fatal("new sweep launched after cancellation")
	}
}

func TestReadonlyMaintenanceCancelledBeforeStartHasNoEffect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	runReadonlyMaintenance(ctx, time.Millisecond, func(context.Context, string) (string, error) { called = true; return "", nil }, func(error) { t.Fatal("cancelled sweep reported error") })
	if called {
		t.Fatal("cancelled service performed root operation")
	}
}
