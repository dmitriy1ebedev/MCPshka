package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// settledIntegrations remembers launches whose claim is terminal (retired or
// FAILED) in this broker incarnation. Without it every 30 s sweep re-proved
// all historical launches (branch retire + many sqlite reads) and, under test
// load, hit the sweep deadline forever (DIAG ACTIVE-01, MCP-125). A restart
// forgets the memo, so each launch is still re-proved once per incarnation.
var settledIntegrations sync.Map

// integrationSweepFailures counts consecutive failed sweeps for health.
var integrationSweepFailures atomic.Int64

// IntegrationMaintenanceFailures reports consecutive failed recovery sweeps.
func IntegrationMaintenanceFailures() int64 { return integrationSweepFailures.Load() }

const (
	integrationPageSize   = 32
	integrationPerSweep   = 4
	integrationItemBudget = 20 * time.Second
	integrationSweepLimit = 75 * time.Second
)

type integrationMaintenanceBackend interface {
	Status(context.Context, string) (IntegrationRuntime, error)
	Fence(context.Context, string) (IntegrationRuntime, error)
	RecoverFence(context.Context, string) (IntegrationRuntime, error)
	Recover(context.Context, string) (IntegrationClaim, error)
	Abort(context.Context, string) (IntegrationClaim, error)
	Retire(context.Context, string) error
}
type liveIntegrationMaintenance struct{}

func (liveIntegrationMaintenance) Status(ctx context.Context, id string) (IntegrationRuntime, error) {
	return IntegrationRuntimeStatus(ctx, id)
}
func (liveIntegrationMaintenance) Fence(ctx context.Context, id string) (IntegrationRuntime, error) {
	return FenceIntegrationWorker(ctx, id)
}
func (liveIntegrationMaintenance) RecoverFence(ctx context.Context, id string) (IntegrationRuntime, error) {
	return RecoverIntegrationWorker(ctx, id)
}
func (liveIntegrationMaintenance) Recover(ctx context.Context, id string) (IntegrationClaim, error) {
	return RecoverIntegration(ctx, id)
}
func (liveIntegrationMaintenance) Abort(ctx context.Context, id string) (IntegrationClaim, error) {
	return AbortProtectedIntegration(ctx, id)
}
func (liveIntegrationMaintenance) Retire(ctx context.Context, id string) error {
	_, err := RetireOwnedGitBranch(ctx, id)
	return err
}

// Only already-dispatched, root-sealed integrations are continued. Neither
// untrusted queue rows nor elapsed time authorize launching or signalling.
func MaintainIntegrationWorkers(ctx context.Context, after string) (string, error) {
	if err := ctx.Err(); err != nil {
		return after, err
	}
	c, err := OpenAuthority()
	if err != nil {
		return after, err
	}
	keys, err := c.db.ListPage(ctx, "executor/integration-launches/", after, integrationPageSize)
	if err != nil {
		return after, err
	}
	if len(keys) == 0 && after != "" {
		keys, err = c.db.ListPage(ctx, "executor/integration-launches/", "", integrationPageSize)
		if err != nil {
			return after, err
		}
	}
	if len(keys) == 0 {
		return "", nil
	}
	var failed error
	worked := 0
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return after, err
		}
		permit := strings.TrimSuffix(strings.TrimPrefix(key, "executor/integration-launches/"), ".json")
		if _, done := settledIntegrations.Load(permit); done {
			after = key
			continue
		}
		if worked == integrationPerSweep {
			break
		}
		worked++
		after = key
		if !idPattern.MatchString(permit) {
			failed = ErrHold
			continue
		}
		m, err := c.readIntegrationLaunch(ctx, permit)
		if err != nil || m.State != "READY" {
			failed = ErrHold
			continue
		}
		// Each launch gets its own bounded budget: one slow proof no longer
		// starves the rest of the page nor the next sweep.
		item, cancel := context.WithTimeout(ctx, integrationItemBudget)
		settled, err := maintainIntegrationWorkerSettled(item, m.ApprovalID, liveIntegrationMaintenance{})
		cancel()
		if settled {
			settledIntegrations.Store(permit, struct{}{})
		}
		if err != nil && !errors.Is(err, ErrHold) {
			failed = err
		}
	}
	return after, failed
}

func maintainIntegrationWorker(ctx context.Context, id string, b integrationMaintenanceBackend) error {
	_, err := maintainIntegrationWorkerSettled(ctx, id, b)
	return err
}

// maintainIntegrationWorkerSettled also reports whether the claim reached a
// terminal state that no later sweep can change.
func maintainIntegrationWorkerSettled(ctx context.Context, id string, b integrationMaintenanceBackend) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !digestPattern.MatchString(id) || b == nil {
		return false, ErrHold
	}
	runtime, err := b.Status(ctx, id)
	if err != nil {
		return false, err
	}
	if runtime.Claim.ApprovalID != id || runtime.Launch.ApprovalID != id || !sameIntegrationClaim(runtime.Claim, runtime.Launch.Claim) || runtime.Worker.Pin != runtime.Launch.Pin || len(runtime.Worker.Launches) != 1 || runtime.Worker.Launches[0].ID != runtime.Launch.LaunchID {
		return false, ErrHold
	}
	if runtime.Claim.State == "RELEASED" {
		return retired(b.Retire(ctx, id))
	}
	if runtime.Claim.State == "FAILED" {
		return true, nil
	}
	if !runtime.Dispatched || (runtime.Claim.State != "HELD" && runtime.Claim.State != "EFFECT_INTENT") {
		return false, ErrHold
	}
	if runtime.Worker.State != "FENCED" {
		if runtime.Worker.Result != nil {
			// A trusted leader already persisted its child's bounded terminal result.
			// A fabricated/stale result cannot authorize stopping a live cgroup.
			if !validResult(*runtime.Worker.Result, runtime.Worker) {
				return false, ErrHold
			}
			runtime, err = b.Fence(ctx, id)
		} else {
			// No exit result: observe the exact stopped incarnation, never signal.
			runtime, err = b.RecoverFence(ctx, id)
		}
		if err != nil {
			return false, err
		}
		if runtime.Worker.State != "FENCED" || runtime.Claim.ApprovalID != id {
			return false, ErrHold
		}
	}
	// Git/result reconciliation decides success; absence of stdout/exit does not.
	claim, err := b.Recover(ctx, id)
	if err == nil {
		if claim.State != "RELEASED" || !sameIntegrationClaim(claim, runtime.Claim) {
			return false, ErrHold
		}
		return retired(b.Retire(ctx, id))
	}
	if !errors.Is(err, ErrHold) {
		return false, err
	}
	// The abort path independently proves fresh stopped fencing and no effect.
	// ATTEMPT or ambiguous Git stays HOLD; failed work never becomes success.
	claim, err = b.Abort(ctx, id)
	if err != nil {
		return false, err
	}
	if claim.State != "FAILED" || !sameIntegrationClaim(claim, runtime.Claim) {
		return false, ErrHold
	}
	return true, nil
}

func retired(err error) (bool, error) { return err == nil, err }

func RunIntegrationMaintenance(ctx context.Context, report func(error)) {
	log := throttledReport(report)
	runMaintenanceLoop(ctx, 30*time.Second, integrationSweepLimit, MaintainIntegrationWorkers, func(err error) {
		if err == nil {
			integrationSweepFailures.Store(0)
		} else {
			integrationSweepFailures.Add(1)
		}
		log(err)
	})
}
