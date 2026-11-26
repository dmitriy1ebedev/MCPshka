package executor

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/unitfence"
	"os"
	"strings"
	"time"
)

// FenceApproval is an immutable target plus an append-only state progression.
// Only the privileged authority can grant permission; the worker cannot mint it.
type FenceApproval struct {
	ID            string    `json:"id"`
	Pin           Pin       `json:"pin"`
	LaunchID      string    `json:"launch_id"`
	Identity      Identity  `json:"identity"`
	Authorization string    `json:"authorization"`
	ApprovedAt    time.Time `json:"approved_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	State         string    `json:"state"`
}

// AdministrativeFenceStatus exposes no holder secret and never changes state.
func (c *Controller) AdministrativeFenceStatus(ctx context.Context, p Pin, id string) (FenceApproval, error) {
	r, _, err := c.readFenceApproval(ctx, p, id)
	return r, err
}

func fenceApprovalKey(id string) string { return "executor/fence-approvals/" + id + ".json" }

// ApproveAdministrativeFence is a root-only administrative entry point, not a
// broker operation. A caller's boolean or syntactically valid ID is never consent.
func (c *Controller) ApproveAdministrativeFence(ctx context.Context, p Pin, id, authorization string, expires time.Time) error {
	if !c.authority || os.Geteuid() != 0 {
		return ErrHold
	}
	return c.WithPermitLock(ctx, p, func() error { return c.approveFence(ctx, p, id, authorization, expires) })
}

func (c *Controller) approveFence(ctx context.Context, p Pin, id, authorization string, expires time.Time) error {
	now := time.Now().UTC()
	if !idPattern.MatchString(id) || strings.TrimSpace(authorization) == "" || len(authorization) > 256 || strings.ContainsAny(authorization, "\r\n\x00") || !expires.After(now) || expires.After(now.Add(15*time.Minute)) {
		return ErrHold
	}
	s, err := c.Inspect(ctx, p)
	if err != nil {
		return err
	}
	if s.State != "ACTIVE" || len(s.Launches) != 1 || s.Launches[0].State != "REGISTERED" || s.Launches[0].Identity == nil {
		return ErrHold
	}
	l := s.Launches[0]
	r := FenceApproval{ID: id, Pin: p, LaunchID: l.ID, Identity: *l.Identity, Authorization: authorization, ApprovedAt: now, ExpiresAt: expires.UTC(), State: "APPROVED"}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	ok, err := c.db.PutIfAbsent(fenceApprovalKey(id), b)
	if err != nil {
		return err
	}
	if !ok {
		return ErrHold
	}
	return nil
}

func (c *Controller) readFenceApproval(ctx context.Context, p Pin, id string) (FenceApproval, []byte, error) {
	if c.db == nil || !validPin(p) || !idPattern.MatchString(id) {
		return FenceApproval{}, nil, ErrHold
	}
	b, err := c.db.GetContext(ctx, fenceApprovalKey(id))
	if errors.Is(err, os.ErrNotExist) {
		return FenceApproval{}, nil, ErrHold
	}
	if err != nil {
		return FenceApproval{}, nil, err
	}
	var r FenceApproval
	if json.Unmarshal(b, &r) != nil || r.ID != id || r.Pin != p || !idPattern.MatchString(r.LaunchID) || !unitfence.ValidIdentity(r.Identity) || !heavyqueue.ExecutorCgroupMatches(r.Identity.Cgroup, r.LaunchID) || r.Authorization == "" || len(r.Authorization) > 256 || strings.ContainsAny(r.Authorization, "\r\n\x00") || r.ApprovedAt.IsZero() || !r.ExpiresAt.After(r.ApprovedAt) || r.ExpiresAt.Sub(r.ApprovedAt) > 15*time.Minute {
		return FenceApproval{}, nil, ErrHold
	}
	switch r.State {
	case "APPROVED", "FENCING", "FENCED":
	default:
		return FenceApproval{}, nil, ErrHold
	}
	return r, b, nil
}

func (c *Controller) replaceFenceApproval(id string, old []byte, r FenceApproval) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	ok, err := c.db.Replace(fenceApprovalKey(id), old, b)
	if err != nil {
		return err
	}
	if !ok {
		return ErrHold
	}
	return nil
}

// AdministrativeFence holds the same cross-process admission lock as launch.
// An interrupted external stop is UNKNOWN, never permission to repeat it.
func (c *Controller) AdministrativeFence(ctx context.Context, p Pin, id string) error {
	if !c.authority || os.Geteuid() != 0 {
		return ErrHold
	}
	return c.WithPermitLock(ctx, p, func() error { return c.administrativeFence(ctx, p, id, SystemdFence{}) })
}

func (c *Controller) administrativeFence(ctx context.Context, p Pin, id string, b FenceBackend) error {
	if b == nil {
		return ErrHold
	}
	r, raw, err := c.readFenceApproval(ctx, p, id)
	if err != nil {
		return err
	}
	s, err := c.Inspect(ctx, p)
	if err != nil {
		return err
	}
	if len(s.Launches) != 1 || s.Launches[0].ID != r.LaunchID || s.Launches[0].Identity == nil || *s.Launches[0].Identity != r.Identity {
		return ErrHold
	}
	if r.State == "FENCED" {
		if s.State != "FENCED" || s.Launches[0].State != "FENCED" {
			return ErrHold
		}
		return nil
	}
	if r.State == "FENCING" {
		if s.State != "FENCED" {
			if err = c.reconcileAdministrativeFence(ctx, p, r, b); err != nil {
				return err
			}
		} else if s.Launches[0].State != "FENCED" {
			return ErrHold
		}
		r.State = "FENCED"
		return c.replaceFenceApproval(id, raw, r)
	}
	if !r.ExpiresAt.After(time.Now().UTC()) || r.ApprovedAt.After(time.Now().UTC()) || s.State != "ACTIVE" || s.Launches[0].State != "REGISTERED" {
		return ErrHold
	}
	r.State = "FENCING"
	if err = c.replaceFenceApproval(id, raw, r); err != nil {
		return err
	}
	if err = c.Fence(ctx, p, b); err != nil {
		return err
	}
	r, raw, err = c.readFenceApproval(ctx, p, id)
	if err != nil || r.State != "FENCING" {
		return ErrHold
	}
	r.State = "FENCED"
	return c.replaceFenceApproval(id, raw, r)
}
