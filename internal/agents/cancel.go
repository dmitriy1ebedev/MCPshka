package agents

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"mcpshka/internal/unitbus"
)

// unitControl stops and observes one systemd unit; tests swap it.
type unitControl struct {
	stop  func(ctx context.Context, unit string) error
	ended func(ctx context.Context, unit string) (bool, error)
}

var systemdUnits = unitControl{
	stop: func(ctx context.Context, unit string) error {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return exec.CommandContext(c, "/usr/bin/systemctl", "stop", unit).Run()
	},
	ended: func(ctx context.Context, unit string) (bool, error) {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		raw, err := unitbus.Output(c, "show", "--property=ActiveState", "--value", unit)
		state := strings.TrimSpace(string(raw))
		return err == nil && (state == "inactive" || state == "failed"), err
	},
}

// Cancel is the bounded owner stop for one delegated task. Runner and attempt
// units are stopped and must both be observed ended before the record says
// "cancelled"; any stop or probe failure leaves the record unchanged and
// returns UNKNOWN, so a live writer never looks free. Work already written to
// the repository is kept: its effect stays unknown until git is inspected.
//
// The work is split by who may do it (13.11: the broker failed with
// "permission denied" on the record): root stops the units (StopUnits), the
// server, which owns /var/lib/mcpshka/agent-tasks, writes the record
// (Cancel). The broker has no CAP_DAC_OVERRIDE and that tree is read-only to it.
func Cancel(ctx context.Context, id string) (Task, error) {
	t, err := ReadTask(id)
	if err != nil {
		return t, err
	}
	observe := unitControl{stop: func(context.Context, string) error { return nil }, ended: systemdUnits.ended}
	t, changed, err := cancelTask(ctx, t, observe, time.Now().UTC())
	if err != nil || !changed {
		return t, err
	}
	return t, saveTask(t)
}

// StopUnits is root's part: stop the runner and attempt units and see both
// ended. It reads no record.
func StopUnits(ctx context.Context, id string) error {
	if !ValidTaskID(id) {
		return errors.New("invalid task id")
	}
	_, _, err := cancelTask(ctx, Task{ID: id, State: "running"}, systemdUnits, time.Now().UTC())
	return err
}

func cancelTask(ctx context.Context, t Task, u unitControl, now time.Time) (Task, bool, error) {
	if t.State == "done" || t.State == "cancelled" {
		return t, false, nil
	}
	units := []string{"mcpshka-agent-task-runner-" + t.ID + ".service", "mcpshka-agent-task-" + t.ID + ".service"}
	for _, unit := range units {
		if err := u.stop(ctx, unit); err != nil {
			if ended, probeErr := u.ended(ctx, unit); probeErr != nil || !ended {
				return t, false, errors.New("stop of " + unit + " UNKNOWN; task record unchanged, observe agent_task_status before retrying")
			}
		}
	}
	for _, unit := range units {
		if ended, err := u.ended(ctx, unit); err != nil || !ended {
			return t, false, errors.New(unit + " still active or unobservable after stop: HOLD; task record unchanged")
		}
	}
	// The runner may have written a terminal record while it was stopping.
	if fresh, err := ReadTask(t.ID); err == nil && (fresh.State == "done" || fresh.State == "cancelled") {
		return fresh, false, nil
	}
	previous := t.State
	t.State, t.Queue, t.ResumeAt = "cancelled", nil, time.Time{}
	if t.FinishedAt.IsZero() {
		t.FinishedAt = now
	}
	t.Error = "cancelled by owner from state " + previous + "; runner and attempt units observed ended"
	t.NextAction = "inspect git status/diff, project_lease_status and jobs of " + t.Path + " before any agent_task resume_of=" + t.ID
	return t, true, nil
}
