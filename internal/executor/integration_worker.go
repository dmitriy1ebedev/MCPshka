package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/unitfence"
	"os"
	"strings"
	"syscall"
	"time"
)

// IntegrationWorker is a fixed privileged entrypoint: no command, path, result,
// or writer token can be supplied by the daemon. Only sealed IDs are accepted.
func IntegrationWorker(ctx context.Context, permit, launch string) error {
	c, m, err := OpenIntegrationLaunch(ctx, permit, launch)
	if err != nil {
		return err
	}
	identity, err := integrationWorkerIdentity(ctx, launch)
	if err != nil {
		return err
	}
	a, err := IntegrationApprovalStatus(ctx, m.ApprovalID)
	if err != nil {
		return ErrHold
	}
	work, cancel := context.WithTimeout(ctx, 660*time.Second)
	defer cancel()
	registered := false
	err = c.WithPermitLock(work, m.Pin, func() error {
		active, e := c.Inspect(work, m.Pin)
		if e != nil || active.State != "ACTIVE" {
			return ErrHold
		}
		if err := c.Register(work, m.Pin, launch, identity); err != nil {
			return err
		}
		registered = true
		if _, err := ImportProtectedIntegration(work, a.ID); err != nil {
			return err
		}
		return c.withIntegrationLock(work, a.Repository, func() error {
			if _, err := c.protectedRepositoryBirth(work, ProtectedRepositoryRoot, a.Repository, 0); err != nil {
				return ErrHold
			}
			if err := c.authenticateIntegrationWorker(work, m, a, identity); err != nil {
				return err
			}
			if err := c.initializeProtectedTarget(work, ProtectedRepositoryRoot, a, 0); err != nil {
				return err
			}
			if err := c.beginIntegrationEffect(work, m.Claim); err != nil {
				return err
			}
			return c.applyProtectedRefEffect(work, ProtectedRepositoryRoot, a, m.Claim.Generation, 0, nil)
		})
	})
	if !registered {
		return err
	}
	code := 0
	output := "protected integration effect recorded; worker remains unfenced"
	if err != nil {
		code = 1
		output = "protected integration held or failed: " + fmt.Sprintf("%.300s", err.Error())
	}
	result := ExecutionResult{Pin: m.Pin, LaunchID: launch, Identity: identity, ExitCode: code, WaitFailed: err != nil, Output: output, ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}
	if e := persistWorkerExit(ctx, func(export context.Context) error {
		return c.WithPermitLock(export, m.Pin, func() error { return c.recordResult(export, result) })
	}); e != nil {
		return e
	}
	// A stored successful effect is not completion. Keep the exact registered
	// leader visible until the broker drains and verifies its cgroup.
	<-ctx.Done()
	return ctx.Err()
}
func integrationWorkerIdentity(ctx context.Context, launch string) (Identity, error) {
	unit, err := unitfence.UnitEvidence(ctx, launch)
	if err != nil {
		return Identity{}, ErrHold
	}
	props := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(unit), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return Identity{}, ErrHold
		}
		if _, dup := props[k]; dup {
			return Identity{}, ErrHold
		}
		props[k] = v
	}
	boot, err := unitfence.ReadSmall("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Identity{}, ErrHold
	}
	ticks, err := unitfence.ProcessTicks(os.Getpid())
	if err != nil {
		return Identity{}, ErrHold
	}
	cg := heavyqueue.ExecutorCgroup(launch)
	info, err := os.Stat("/sys/fs/cgroup" + cg)
	if err != nil {
		return Identity{}, ErrHold
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, ErrHold
	}
	identity := Identity{BootID: strings.TrimSpace(string(boot)), InvocationID: props["InvocationID"], Cgroup: cg, CgroupDevice: uint64(st.Dev), CgroupInode: st.Ino, PID: os.Getpid(), StartTicks: ticks}
	if !unitfence.ValidIdentity(identity) || unitfence.VerifyUnit(unit, identity, false) != nil {
		return Identity{}, ErrHold
	}
	return identity, nil
}

// Invoked under both sealed permit admission and root repository locks.
func (c *Controller) authenticateIntegrationWorker(ctx context.Context, m IntegrationLaunch, a IntegrationApproval, i Identity) error {
	runtime, e := c.integrationRuntime(ctx, a)
	if e != nil || !runtime.Dispatched {
		return ErrHold
	}
	sealed, err := c.readIntegrationLaunch(ctx, m.Pin.PermitID)
	if err != nil || sealed != m || m.State != "READY" || !claimMatchesApproval(m.Claim, a) {
		return ErrHold
	}
	claim, _, err := c.readIntegrationClaim(ctx, a.Repository.CommonDir)
	if err != nil || !sameIntegrationClaim(claim, m.Claim) || claim.State != "HELD" {
		return ErrHold
	}
	raw, err := c.db.GetContext(ctx, integrationWorkerKey(a.ExecutionID))
	expected, _ := json.Marshal(integrationWorkerBinding{Version: 1, Claim: m.Claim, Pin: m.Pin, LaunchID: m.LaunchID})
	if err != nil || !bytes.Equal(raw, expected) {
		return ErrHold
	}
	s, err := c.Inspect(ctx, m.Pin)
	if err != nil || s.State != "ACTIVE" || len(s.Launches) != 1 {
		return ErrHold
	}
	l := s.Launches[0]
	if l.ID != m.LaunchID || l.State != "REGISTERED" || l.Identity == nil || *l.Identity != i {
		return ErrHold
	}
	raw, err = c.db.GetContext(ctx, protectedImportKey(a.ID))
	var imported ProtectedImport
	if err != nil || len(raw) > 4096 {
		return ErrHold
	}
	// Decode strictly; no caller-provided success is consumed here.
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&imported) != nil || d.Decode(new(any)) != io.EOF || imported != (ProtectedImport{Version: 1, ApprovalID: a.ID, Repository: a.Repository, Candidate: a.Request.Commit, Target: a.Request.TargetHead, State: "VERIFIED"}) {
		return ErrHold
	}
	return nil
}
