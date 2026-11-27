package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strconv"
)

type IntegrationLaunch struct {
	Version    int              `json:"version"`
	ApprovalID string           `json:"approval_id"`
	Claim      IntegrationClaim `json:"claim"`
	Pin        Pin              `json:"pin"`
	LaunchID   string           `json:"launch_id"`
	State      string           `json:"state"`
}

func integrationLaunchKey(permit string) string {
	return "executor/integration-launches/" + permit + ".json"
}
func integrationLaunchIdentity(domain string, claim IntegrationClaim) string {
	sum := sha256.Sum256([]byte("mcpshka-integration-v1/" + domain + "/" + claim.ApprovalID + "/" + strconv.FormatUint(claim.Generation, 10)))
	return hex.EncodeToString(sum[:16])
}

// Root-only broker preparation. Persist and bind all admission before invoking
// systemd. This function never launches a process or retries an unknown launch.
func PrepareIntegrationLaunch(ctx context.Context, id string) (IntegrationLaunch, error) {
	a, err := IntegrationApprovalStatus(ctx, id)
	if err != nil {
		return IntegrationLaunch{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationLaunch{}, err
	}
	var result IntegrationLaunch
	err = c.withIntegrationLock(ctx, a.Repository, func() error {
		if _, e := c.protectedRepositoryBirth(ctx, ProtectedRepositoryRoot, a.Repository, 0); e != nil {
			return ErrHold
		}
		var e error
		result, e = c.prepareIntegrationLaunch(ctx, a)
		return e
	})
	return result, err
}
func (c *Controller) prepareIntegrationLaunch(ctx context.Context, a IntegrationApproval) (IntegrationLaunch, error) {
	claim, err := c.reserveIntegrationClaim(ctx, a)
	if err != nil {
		return IntegrationLaunch{}, err
	}
	if claim.State != "HELD" {
		return IntegrationLaunch{}, ErrHold
	}
	pin := Pin{PermitID: integrationLaunchIdentity("permit", claim), LeaseID: integrationLaunchIdentity("lease", claim), Generation: claim.Generation, ExecutionID: claim.ExecutionID}
	launch := IntegrationLaunch{Version: 1, ApprovalID: a.ID, Claim: claim, Pin: pin, LaunchID: integrationLaunchIdentity("launch", claim), State: "PREPARE_INTENT"}
	if pin.PermitID == a.TestPin.PermitID || launch.LaunchID == a.LaunchID {
		return IntegrationLaunch{}, ErrHold
	}
	raw, _ := json.Marshal(launch)
	fresh, err := c.db.PutIfAbsent(integrationLaunchKey(pin.PermitID), raw)
	if err != nil {
		return IntegrationLaunch{}, err
	}
	if !fresh {
		old, err := c.readIntegrationLaunch(ctx, pin.PermitID)
		if err != nil {
			return IntegrationLaunch{}, err
		}
		state := old.State
		old.State = "PREPARE_INTENT"
		if old != launch || (state != "PREPARE_INTENT" && state != "READY") {
			return IntegrationLaunch{}, ErrHold
		}
		if state == "READY" {
			s, e := c.Inspect(ctx, pin)
			if e != nil || s.State != "ACTIVE" || len(s.Launches) != 1 || s.Launches[0].ID != launch.LaunchID || s.Launches[0].State != "LAUNCH_INTENT" || s.Launches[0].Identity != nil {
				return IntegrationLaunch{}, ErrHold
			}
			binding, _ := json.Marshal(pin)
			global, e := c.db.GetContext(ctx, "executor/launches/"+launch.LaunchID+".json")
			if e != nil || !bytes.Equal(global, binding) {
				return IntegrationLaunch{}, ErrHold
			}
			expected, _ := json.Marshal(integrationWorkerBinding{Version: 1, Claim: claim, Pin: pin, LaunchID: launch.LaunchID})
			worker, e := c.db.GetContext(ctx, integrationWorkerKey(claim.ExecutionID))
			if e != nil || !bytes.Equal(worker, expected) {
				return IntegrationLaunch{}, ErrHold
			}
			old.State = "READY"
			return old, nil
		}
	}
	// Exact sealed creation intent allows recovery of a split generation/permit
	// write. A different binding or snapshot is never repaired or overwritten.
	binding, _ := json.Marshal(pin)
	made, err := c.db.PutIfAbsent(generationKey(pin), binding)
	if err != nil {
		return IntegrationLaunch{}, err
	}
	if !made {
		old, e := c.db.GetContext(ctx, generationKey(pin))
		if e != nil || !bytes.Equal(old, binding) {
			return IntegrationLaunch{}, ErrHold
		}
	}
	snapshot, _ := json.Marshal(Snapshot{Pin: pin, Revision: 1, State: "ACTIVE", Launches: []Launch{}})
	if _, err := c.db.PutIfAbsent(key(pin), snapshot); err != nil {
		return IntegrationLaunch{}, err
	}
	s, err := c.Inspect(ctx, pin)
	if err != nil || s.State != "ACTIVE" || len(s.Launches) > 1 {
		return IntegrationLaunch{}, ErrHold
	}
	// Only PREPARE_INTENT can recover the split launch binding/snapshot write.
	// READY is checked above and never repairs lost authority.
	globalKey := "executor/launches/" + launch.LaunchID + ".json"
	created, err := c.db.PutIfAbsent(globalKey, binding)
	if err != nil {
		return IntegrationLaunch{}, err
	}
	if !created {
		prior, e := c.db.GetContext(ctx, globalKey)
		if e != nil || !bytes.Equal(prior, binding) {
			return IntegrationLaunch{}, ErrHold
		}
	}
	if len(s.Launches) == 0 {
		if err := c.change(ctx, pin, func(current *Snapshot) error {
			if current.State != "ACTIVE" || len(current.Launches) != 0 {
				return ErrHold
			}
			current.Launches = []Launch{{ID: launch.LaunchID, State: "LAUNCH_INTENT"}}
			return nil
		}); err != nil {
			return IntegrationLaunch{}, err
		}
	} else if s.Launches[0].ID != launch.LaunchID || s.Launches[0].State != "LAUNCH_INTENT" || s.Launches[0].Identity != nil {
		return IntegrationLaunch{}, ErrHold
	}
	if err := c.bindIntegrationWorker(ctx, claim, pin, launch.LaunchID); err != nil {
		return IntegrationLaunch{}, err
	}
	launch.State = "READY"
	raw, _ = json.Marshal(launch)
	if err := c.db.Put(integrationLaunchKey(pin.PermitID), raw); err != nil {
		return IntegrationLaunch{}, err
	}
	return launch, nil
}
func (c *Controller) readIntegrationLaunch(ctx context.Context, permit string) (IntegrationLaunch, error) {
	if !idPattern.MatchString(permit) {
		return IntegrationLaunch{}, ErrHold
	}
	raw, err := c.db.GetContext(ctx, integrationLaunchKey(permit))
	if err != nil || len(raw) > 8192 {
		return IntegrationLaunch{}, ErrHold
	}
	var m IntegrationLaunch
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.Version != 1 || m.ApprovalID != m.Claim.ApprovalID || !validIntegrationClaim(m.Claim, m.Claim.Repository.CommonDir) || m.Claim.State != "HELD" || !validPin(m.Pin) || m.Pin.PermitID != permit || m.Pin.ExecutionID != m.Claim.ExecutionID || m.Pin.Generation != m.Claim.Generation || m.Pin.PermitID != integrationLaunchIdentity("permit", m.Claim) || m.Pin.LeaseID != integrationLaunchIdentity("lease", m.Claim) || m.LaunchID != integrationLaunchIdentity("launch", m.Claim) || (m.State != "PREPARE_INTENT" && m.State != "READY") {
		return IntegrationLaunch{}, ErrHold
	}
	return m, nil
}

// Opens only sealed root authority. It does not relax the existing untrusted
// test/canary manifest contract to permit privileged integration commands.
func OpenIntegrationLaunch(ctx context.Context, permit, launch string) (*Controller, IntegrationLaunch, error) {
	if os.Geteuid() != 0 || !idPattern.MatchString(launch) {
		return nil, IntegrationLaunch{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return nil, IntegrationLaunch{}, err
	}
	m, err := c.readIntegrationLaunch(ctx, permit)
	if err != nil || m.State != "READY" || m.LaunchID != launch {
		return nil, IntegrationLaunch{}, ErrHold
	}
	a, err := IntegrationApprovalStatus(ctx, m.ApprovalID)
	if err != nil || !claimMatchesApproval(m.Claim, a) {
		return nil, IntegrationLaunch{}, ErrHold
	}
	current, _, err := c.readIntegrationClaim(ctx, a.Repository.CommonDir)
	if err != nil || !sameIntegrationClaim(current, m.Claim) || (current.State != "HELD" && current.State != "EFFECT_INTENT") {
		return nil, IntegrationLaunch{}, ErrHold
	}
	raw, err := c.db.GetContext(ctx, integrationWorkerKey(m.Claim.ExecutionID))
	if err != nil {
		return nil, IntegrationLaunch{}, ErrHold
	}
	expected, _ := json.Marshal(integrationWorkerBinding{Version: 1, Claim: m.Claim, Pin: m.Pin, LaunchID: m.LaunchID})
	if !bytes.Equal(raw, expected) {
		return nil, IntegrationLaunch{}, ErrHold
	}
	s, err := c.Inspect(ctx, m.Pin)
	if err != nil || s.State != "ACTIVE" || len(s.Launches) != 1 || s.Launches[0].ID != launch {
		return nil, IntegrationLaunch{}, ErrHold
	}
	if _, err := c.protectedRepositoryBirth(ctx, ProtectedRepositoryRoot, a.Repository, 0); err != nil {
		return nil, IntegrationLaunch{}, ErrHold
	}
	runtime, err := c.integrationRuntime(ctx, a)
	if err != nil || !runtime.Dispatched {
		return nil, IntegrationLaunch{}, ErrHold
	}
	return c, m, nil
}
