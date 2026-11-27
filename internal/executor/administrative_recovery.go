package executor

import (
	"context"
	"encoding/json"
)

type stoppedObserver interface {
	VerifyStopped(context.Context, string, Identity) error
}

// Reconciliation never retries the unknown external stop. The trusted root
// controller closes launch admission and verifies only the original incarnation.
func (c *Controller) reconcileAdministrativeFence(ctx context.Context, p Pin, r FenceApproval, b FenceBackend) error {
	binding, err := c.db.GetContext(ctx, "executor/launches/"+r.LaunchID+".json")
	if err != nil {
		return ErrHold
	}
	var bound Pin
	if json.Unmarshal(binding, &bound) != nil || bound != p {
		return ErrHold
	}
	if err = c.Close(ctx, p); err != nil {
		return err
	}
	s, err := c.Inspect(ctx, p)
	if err != nil {
		return err
	}
	if len(s.Launches) != 1 || s.Launches[0].ID != r.LaunchID || s.Launches[0].Identity == nil || *s.Launches[0].Identity != r.Identity {
		return ErrHold
	}
	observer, ok := b.(stoppedObserver)
	if !ok || observer.VerifyStopped(ctx, r.LaunchID, r.Identity) != nil {
		return ErrHold
	}
	if s.Launches[0].State != "FENCED" {
		if err = c.change(ctx, p, func(s *Snapshot) error {
			if s.State != "REVOKING" || len(s.Launches) != 1 || s.Launches[0].State != "REGISTERED" || s.Launches[0].ID != r.LaunchID || s.Launches[0].Identity == nil || *s.Launches[0].Identity != r.Identity {
				return ErrHold
			}
			s.Launches[0].State = "FENCED"
			return nil
		}); err != nil {
			return err
		}
	}
	return c.CompleteFence(ctx, p)
}
