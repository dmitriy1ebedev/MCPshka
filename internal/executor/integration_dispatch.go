package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
)

type IntegrationRuntime struct {
	Launch     IntegrationLaunch `json:"launch"`
	Claim      IntegrationClaim  `json:"claim"`
	Worker     Snapshot          `json:"worker"`
	Dispatched bool              `json:"dispatched"`
}
type integrationDispatch struct {
	Version int               `json:"version"`
	Launch  IntegrationLaunch `json:"launch"`
	State   string            `json:"state"`
}

func integrationDispatchKey(launch string) string {
	return "executor/integration-dispatches/" + launch + ".json"
}

// No START_INTENT is ever replayed. Lost scheduling replies are reconciled by
// status/fencing, not by starting another unit with a different identifier.
func (c *Controller) recordIntegrationDispatch(ctx context.Context, m IntegrationLaunch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sealed, err := c.readIntegrationLaunch(ctx, m.Pin.PermitID)
	if err != nil || sealed != m || m.State != "READY" {
		return ErrHold
	}
	a, err := c.integrationApprovalForID(ctx, m.ApprovalID)
	if err != nil || !claimMatchesApproval(m.Claim, a) {
		return ErrHold
	}
	claim, _, err := c.readIntegrationClaim(ctx, m.Claim.Repository.CommonDir)
	if err != nil || !sameIntegrationClaim(claim, m.Claim) || claim.State != "HELD" {
		return ErrHold
	}
	s, err := c.Inspect(ctx, m.Pin)
	if err != nil || s.State != "ACTIVE" || len(s.Launches) != 1 || s.Launches[0].ID != m.LaunchID || s.Launches[0].State != "LAUNCH_INTENT" || s.Launches[0].Identity != nil {
		return ErrHold
	}
	expected, _ := json.Marshal(integrationWorkerBinding{Version: 1, Claim: m.Claim, Pin: m.Pin, LaunchID: m.LaunchID})
	raw, err := c.db.GetContext(ctx, integrationWorkerKey(m.Claim.ExecutionID))
	if err != nil || !bytes.Equal(raw, expected) {
		return ErrHold
	}
	binding, _ := json.Marshal(m.Pin)
	raw, err = c.db.GetContext(ctx, "executor/launches/"+m.LaunchID+".json")
	if err != nil || !bytes.Equal(raw, binding) {
		return ErrHold
	}
	raw, _ = json.Marshal(integrationDispatch{Version: 1, Launch: m, State: "START_INTENT"})
	fresh, err := c.db.PutIfAbsent(integrationDispatchKey(m.LaunchID), raw)
	if err != nil {
		return err
	}
	if !fresh {
		return ErrHold
	}
	return nil
}

// Root-broker only: permit -> repository lock ordering matches the worker.
// The callback is a compiled trusted fixed-unit launcher, never caller code.
func (c *Controller) DispatchIntegration(ctx context.Context, m IntegrationLaunch, start func() error) error {
	if !c.authority || os.Geteuid() != 0 || start == nil {
		return ErrHold
	}
	return c.WithPermitLock(ctx, m.Pin, func() error {
		err := c.withIntegrationLock(ctx, m.Claim.Repository, func() error {
			if _, err := c.protectedRepositoryBirth(ctx, ProtectedRepositoryRoot, m.Claim.Repository, 0); err != nil {
				return ErrHold
			}
			return c.recordIntegrationDispatch(ctx, m)
		})
		if err != nil {
			return err
		}
		return start()
	})
}
func (c *Controller) integrationRuntime(ctx context.Context, a IntegrationApproval) (IntegrationRuntime, error) {
	claim, err := c.integrationClaimForApproval(ctx, a)
	if err != nil {
		return IntegrationRuntime{}, ErrHold
	}
	permit := integrationLaunchIdentity("permit", claim)
	m, err := c.readIntegrationLaunch(ctx, permit)
	if err != nil || m.State != "READY" || !sameIntegrationClaim(m.Claim, claim) || m.ApprovalID != a.ID {
		return IntegrationRuntime{}, ErrHold
	}
	raw, err := c.db.GetContext(ctx, integrationWorkerKey(claim.ExecutionID))
	expected, _ := json.Marshal(integrationWorkerBinding{Version: 1, Claim: m.Claim, Pin: m.Pin, LaunchID: m.LaunchID})
	if err != nil || !bytes.Equal(raw, expected) {
		return IntegrationRuntime{}, ErrHold
	}
	worker, err := c.Inspect(ctx, m.Pin)
	if err != nil || len(worker.Launches) != 1 || worker.Launches[0].ID != m.LaunchID {
		return IntegrationRuntime{}, ErrHold
	}
	result := IntegrationRuntime{Launch: m, Claim: claim, Worker: worker}
	raw, err = c.db.GetContext(ctx, integrationDispatchKey(m.LaunchID))
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil || len(raw) > 8192 {
		return IntegrationRuntime{}, ErrHold
	}
	var dispatch integrationDispatch
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&dispatch) != nil || d.Decode(new(any)) != io.EOF || dispatch != (integrationDispatch{Version: 1, Launch: m, State: "START_INTENT"}) {
		return IntegrationRuntime{}, ErrHold
	}
	result.Dispatched = true
	return result, nil
}
func IntegrationRuntimeStatus(ctx context.Context, id string) (IntegrationRuntime, error) {
	a, err := IntegrationApprovalStatus(ctx, id)
	if err != nil {
		return IntegrationRuntime{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationRuntime{}, err
	}
	return c.integrationRuntime(ctx, a)
}
func FenceIntegrationWorker(ctx context.Context, id string) (IntegrationRuntime, error) {
	a, err := IntegrationApprovalStatus(ctx, id)
	if err != nil {
		return IntegrationRuntime{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationRuntime{}, err
	}
	runtime, err := c.integrationRuntime(ctx, a)
	if err != nil {
		return IntegrationRuntime{}, err
	}
	err = c.WithPermitLock(ctx, runtime.Launch.Pin, func() error {
		// Exact registered incarnation only; unknown pending launch remains HOLD.
		return c.Fence(ctx, runtime.Launch.Pin, SystemdFence{})
	})
	if err != nil {
		return IntegrationRuntime{}, err
	}
	return c.integrationRuntime(ctx, a)
}
func RecoverIntegrationWorker(ctx context.Context, id string) (IntegrationRuntime, error) {
	a, err := IntegrationApprovalStatus(ctx, id)
	if err != nil {
		return IntegrationRuntime{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationRuntime{}, err
	}
	runtime, err := c.integrationRuntime(ctx, a)
	if err != nil {
		return IntegrationRuntime{}, err
	}
	err = c.WithPermitLock(ctx, runtime.Launch.Pin, func() error { return c.RecoverFence(ctx, runtime.Launch.Pin, SystemdFence{}) })
	if err != nil {
		return IntegrationRuntime{}, err
	}
	return c.integrationRuntime(ctx, a)
}
