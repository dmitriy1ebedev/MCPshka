package agents

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"mcpshka/internal/failure"
)

type busyRelease struct{ busy, calls int }

func (l *busyRelease) Acquire(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (l *busyRelease) Heartbeat(context.Context, string, string, string) error { return nil }
func (l *busyRelease) Release(context.Context, string, string, string) error {
	l.calls++
	if l.calls <= l.busy {
		return failure.RejectedAs(failure.OwnerBusy, "PROJECT_BUSY", "jobs.WithIdleProject", "project job still pending", nil)
	}
	return nil
}

func TestReleaseWaitsForOwnPendingJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &busyRelease{busy: 3}
		if err := releaseWhenIdle(context.Background(), l, "/p", "tok", "k"); err != nil || l.calls != 4 {
			t.Fatal(err, l.calls)
		}
		l = &busyRelease{busy: 1 << 20}
		start := time.Now()
		if err := releaseWhenIdle(context.Background(), l, "/p", "tok", "k"); err == nil || time.Since(start) < releaseWait {
			t.Fatal("a job that never ends must end the wait with its refusal", err)
		}
	})
}
