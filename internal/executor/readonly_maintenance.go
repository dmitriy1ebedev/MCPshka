package executor

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/failure"
	"strings"
	"time"
)

// MaintainReadonlyWorkers reconciles only sealed, root-prepared committed test
// workers. A recorded result permits fencing the exact registered incarnation.
// It cannot prepare/launch code, approve integration,
// release a project lease, or manufacture an exit result. The cursor is just
// fair scan position: losing it never authorizes repeating an external effect.
func MaintainReadonlyWorkers(ctx context.Context, after string) (string, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return after, contextFailure(ctx, "maintenance.MaintainReadonlyWorkers", started, err)
	}
	c, err := OpenAuthority()
	if err != nil {
		return after, contextFailure(ctx, "maintenance.MaintainReadonlyWorkers", started, err)
	}
	keys, err := c.db.ListPage(ctx, "executor/permits/", after, 4)
	if err != nil {
		return after, contextFailure(ctx, "maintenance.MaintainReadonlyWorkers", started, err)
	}
	if len(keys) == 0 && after != "" {
		keys, err = c.db.ListPage(ctx, "executor/permits/", "", 4)
		if err != nil {
			return after, contextFailure(ctx, "maintenance.MaintainReadonlyWorkers", started, err)
		}
	}
	if len(keys) == 0 {
		return "", nil
	}
	var failed error
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return after, contextFailure(ctx, "maintenance.MaintainReadonlyWorkers", started, err)
		}
		after = k
		id := strings.TrimSuffix(strings.TrimPrefix(k, "executor/permits/"), ".json")
		if !idPattern.MatchString(id) {
			failed = Hold(failure.FencedUnknown, "MAINTENANCE_PERMIT_KEY_INVALID", "maintenance.MaintainReadonlyWorkers", "maintenance permit key invalid", nil)
			continue
		}
		raw, err := c.db.GetContext(ctx, k)
		var stored Snapshot
		if err != nil || len(raw) > 8192 || json.Unmarshal(raw, &stored) != nil || stored.Pin.PermitID != id {
			failed = Hold(failure.FencedUnknown, "MAINTENANCE_PERMIT_INVALID", "maintenance.MaintainReadonlyWorkers", "maintenance permit record invalid", err)
			continue
		}
		if stored.State == "FENCED" {
			continue
		}
		m, err := readManifest(id)
		if err != nil {
			failed = Hold(failure.EvidenceMissing, "MAINTENANCE_MANIFEST_MISSING", "maintenance.MaintainReadonlyWorkers", "maintenance manifest unavailable", err)
			continue
		}
		if !integrationTestPurpose(m.Purpose) && m.Purpose != LocalReleasePurpose {
			continue
		}
		err = c.maintainReadonlyPermit(ctx, m, SystemdFence{}, c.WithPermitLock)
		if err != nil && !errors.Is(err, ErrHold) {
			failed = err
		}
	}
	return after, failed
}

func (c *Controller) maintainReadonlyPermit(ctx context.Context, m Manifest, b FenceBackend, lock func(context.Context, Pin, func() error) error) error {
	started := time.Now()
	if (!integrationTestPurpose(m.Purpose) && m.Purpose != LocalReleasePurpose) || len(m.Runs) != 1 || lock == nil {
		return Hold(failure.PolicyDenied, "MAINTENANCE_INPUT_INVALID", "maintenance.maintainReadonlyPermit", "maintenance purpose, run count or lock invalid", nil)
	}
	s, err := c.Inspect(ctx, m.Pin)
	if err != nil {
		return err
	}
	if s.State == "FENCED" {
		return nil
	}
	if len(s.Launches) != 1 || s.Launches[0].Identity == nil {
		return Hold(failure.FencedUnknown, "MAINTENANCE_LAUNCH_UNBOUND", "maintenance.maintainReadonlyPermit", "maintenance launch identity unavailable", nil)
	}
	l := s.Launches[0]
	if _, ok := m.Runs[l.ID]; !ok || (l.State != "REGISTERED" && l.State != "FENCED") {
		return Hold(failure.FencedUnknown, "MAINTENANCE_RUN_MISMATCH", "maintenance.maintainReadonlyPermit", "maintenance run or state mismatched", nil)
	}
	// A result is not terminal until the existing fence verifies the cgroup.
	// Recheck under the launch lock; a lost stop reply is observation-only recovery.
	if s.State == "ACTIVE" && s.Result != nil {
		return lock(ctx, m.Pin, func() error {
			fresh, err := c.Inspect(ctx, m.Pin)
			if err != nil {
				return err
			}
			if fresh.State == "FENCED" {
				return nil
			}
			if len(fresh.Launches) != 1 || fresh.Launches[0].ID != l.ID || fresh.Launches[0].Identity == nil || *fresh.Launches[0].Identity != *l.Identity || fresh.Result == nil || fresh.Result.LaunchID != l.ID {
				return Hold(failure.FencedUnknown, "MAINTENANCE_IDENTITY_CHANGED", "maintenance.maintainReadonlyPermit", "launch identity or result changed while locking", nil)
			}
			if fresh.State != "ACTIVE" {
				return c.RecoverFence(ctx, m.Pin, b)
			}
			return c.Fence(ctx, m.Pin, b)
		})
	}
	observer, ok := b.(stoppedObserver)
	if !ok {
		return Hold(failure.FencedUnknown, "STOP_OBSERVER_MISSING", "maintenance.maintainReadonlyPermit", "stopped observer unavailable", nil)
	}
	// Do not revoke a live worker merely because maintenance inspected it.
	if err := observer.VerifyStopped(ctx, l.ID, *l.Identity); err != nil {
		return Hold(failure.FencedUnknown, "WORKER_STOP_UNVERIFIED", "maintenance.maintainReadonlyPermit", "worker stop unverified", contextFailure(ctx, "maintenance.maintainReadonlyPermit", started, err))
	}
	return lock(ctx, m.Pin, func() error { return c.RecoverFence(ctx, m.Pin, b) })
}

// RunReadonlyMaintenance belongs to the broker service, independent of MCP/AI.
// Every sweep is bounded; shutdown drains the current sweep.
func RunReadonlyMaintenance(ctx context.Context, report func(error)) {
	runReadonlyMaintenance(ctx, 30*time.Second, MaintainReadonlyWorkers, report)
}

func runReadonlyMaintenance(ctx context.Context, interval time.Duration, maintain func(context.Context, string) (string, error), report func(error)) {
	runMaintenanceLoop(ctx, interval, 10*time.Second, maintain, throttledReport(report))
}

// throttledReport repeats an unchanged error at most every five minutes and
// re-arms after a successful sweep (observe receives nil then).
func throttledReport(report func(error)) func(error) {
	lastError := ""
	nextReport := time.Time{}
	return func(err error) {
		if err == nil {
			lastError = ""
			return
		}
		if report != nil && (err.Error() != lastError || !time.Now().Before(nextReport)) {
			report(err)
			lastError = err.Error()
			nextReport = time.Now().Add(5 * time.Minute)
		}
	}
}

// runMaintenanceLoop sweeps with a bounded budget and hands every sweep's
// outcome (nil on success) to observe; cancellation is never reported.
func runMaintenanceLoop(ctx context.Context, interval, budget time.Duration, maintain func(context.Context, string) (string, error), observe func(error)) {
	cursor := ""
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		sweep, cancel := context.WithTimeout(ctx, budget)
		next, err := maintain(sweep, cursor)
		cancel()
		cursor = next
		if ctx.Err() == nil && observe != nil {
			observe(err)
		}
		timer.Reset(interval)
	}
}
