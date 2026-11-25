package mcpserver

import (
	"context"
	"errors"
	"time"
)

var errMaintenanceActive = errors.New("workflow maintenance drain timed out: HOLD")

// Shutdown waits for the exact maintenance goroutine, not just cancellation of
// its next timer. A drain timeout is unresolved work, never evidence of a gone
// writer and never permission to release its claim.
func startWorkflowMaintenance(interval time.Duration, maintain func() error, report func(error)) func(time.Duration) error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				if err := maintain(); err != nil && report != nil {
					report(err)
				}
			}
		}
	}()
	return func(timeout time.Duration) error {
		cancel()
		select {
		case <-done:
			return nil
		default:
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-done:
			return nil
		case <-timer.C:
			return errMaintenanceActive
		}
	}
}
