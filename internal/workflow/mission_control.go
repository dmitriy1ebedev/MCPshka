package workflow

import (
	"encoding/json"
	"errors"
	"mcpshka/internal/jobs"
	"os"
	"time"
)

func terminal(r Run) bool {
	return r.State == "done" || r.State == "rolled_back" || r.State == "cancelled"
}
func stopped(r Run) bool { return r.Control == "cancelled" || r.Control == "expired" }

// Control and launch admission share the same Run CAS. A launch already
// admitted remains observable; a stale launcher cannot cross a newer stop.
func (e *Engine) updateMission(id, mission string, change func(*Run) error) error {
	if !validDigest(mission, 32) {
		return errors.New("valid mission identity required")
	}
	for range 8 {
		r, err := e.Get(id)
		if err != nil {
			return err
		}
		if r.MissionID != "" && r.MissionID != mission {
			return errors.New("workflow belongs to another mission")
		}
		if !terminal(r) {
			if err := e.requireWorkflowClaim(r); err != nil {
				return err
			}
		}
		next := r
		if err := change(&next); err != nil {
			return err
		}
		before, _ := json.Marshal(r)
		after, _ := json.Marshal(next)
		if string(before) == string(after) {
			return nil
		}
		won, err := e.db.Replace(runKey(id), before, after)
		if err != nil || won {
			return err
		}
	}
	return errors.New("workflow control changed concurrently; reconcile original mission")
}

func (e *Engine) BindMission(id, mission string, deadline *time.Time) error {
	return e.updateMission(id, mission, func(r *Run) error {
		if r.Schema != runSchema {
			return errors.New("legacy workflow must finish before binding new mission controls: HOLD")
		}
		r.MissionID = mission
		if deadline != nil {
			if deadline.IsZero() {
				return errors.New("nonzero mission deadline required")
			}
			if r.Deadline != nil && deadline.After(*r.Deadline) {
				return errors.New("mission deadline cannot be extended")
			}
			pinned := deadline.UTC()
			r.Deadline = &pinned
		}
		return nil
	})
}

func (e *Engine) ControlMission(id, mission, control string) error {
	if control != "running" && control != "paused" && control != "cancelled" {
		return errors.New("unknown mission control")
	}
	return e.updateMission(id, mission, func(r *Run) error {
		if r.MissionID != mission {
			return errors.New("workflow is not bound to this mission")
		}
		if control == "running" && r.Deadline != nil && !time.Now().Before(*r.Deadline) {
			return errors.New("expired mission cannot resume")
		}
		if stopped(*r) {
			if control == "cancelled" && r.Control == "cancelled" {
				return nil
			}
			return errors.New("cancelled or expired mission cannot resume")
		}
		if control == "running" {
			r.Control = ""
		} else {
			r.Control = control
		}
		return nil
	})
}

func (e *Engine) stopBeforeDispatch(r Run) (Run, error) {
	if r.Control == "paused" || !stopped(r) || r.Index >= 10 {
		return r, nil
	}
	if r.Index == 0 && r.LaunchAt.IsZero() {
		next := r
		next.State = "cancelled"
		next.Reason = "mission stopped before any stage admission"
		next, err := e.save(r, next)
		if err == nil {
			err = e.finish(next)
		}
		return next, err
	}
	if r.Index == 0 {
		return e.hold(r, "stopped workflow has an unresolved original admission")
	}
	next := r
	next.Recovery = &RecoveryCause{Index: r.Index - 1, JobID: stageJobID(r.ID, r.Index-1), Supervisor: r.Supervisor, Stopped: true}
	next.Index = 10
	next.State = "rollback"
	next.Reason = "mission stopped; compensate the last verified stage without replay"
	return e.save(r, next)
}

func (e *Engine) releaseUnstarted(r Run) error {
	if r.State != "cancelled" || r.Index != 0 || r.LaunchClaimed || !r.LaunchAt.IsZero() || !stopped(r) {
		return errors.New("unstarted cancellation proof missing: HOLD")
	}
	if _, err := e.manager.Get(stageJobID(r.ID, 0)); !errors.Is(err, jobs.ErrNotFound) {
		return errors.New("unstarted cancellation has a stage receipt: HOLD")
	}
	return e.manager.WithIdleProject(r.Root, func() error {
		owner, err := e.db.Get(claimKey(r.Root))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if string(owner) != r.ID {
			return errors.New("workflow claim changed: HOLD")
		}
		won, err := e.db.Delete(claimKey(r.Root), owner)
		if err == nil && !won {
			return errors.New("workflow claim changed concurrently")
		}
		return err
	})
}
