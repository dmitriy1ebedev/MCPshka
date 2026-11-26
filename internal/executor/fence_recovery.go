package executor

import "context"

// RecoverFence observes a registered incarnation after exit, timeout, reboot,
// or a lost stop reply. It never sends stop/kill and never invents an exit code.
// Production callers must hold the same root permit lock as launch admission.
func (c *Controller) RecoverFence(ctx context.Context, p Pin, backend FenceBackend) error {
	s, err := c.Inspect(ctx, p)
	if err != nil {
		return err
	}
	if s.State == "FENCED" {
		return nil
	}
	if len(s.Launches) != 1 || (s.Launches[0].State != "REGISTERED" && s.Launches[0].State != "FENCED") || s.Launches[0].Identity == nil {
		return ErrHold
	}
	if _, ok := backend.(stoppedObserver); !ok {
		return ErrHold
	}
	if err := c.reconcileAdministrativeFence(ctx, p, FenceApproval{LaunchID: s.Launches[0].ID, Identity: *s.Launches[0].Identity}, backend); err != nil {
		return err
	}
	releaseFencedUnits(ctx, backend, s.Launches)
	return nil
}

// unitReleaser drops a stopped unit's leftover systemd state once its
// fence is recorded; the permit, not the unit, carries the evidence.
type unitReleaser interface {
	ReleaseUnit(context.Context, string)
}

// releaseFencedUnits is best effort and runs only after the fence is durable:
// a failed exit otherwise stays listed as a failed unit forever (BUG-16).
func releaseFencedUnits(ctx context.Context, b FenceBackend, launches []Launch) {
	r, ok := b.(unitReleaser)
	if !ok {
		return
	}
	for _, l := range launches {
		r.ReleaseUnit(ctx, l.ID)
	}
}
