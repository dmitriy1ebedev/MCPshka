package gitprovider

import (
	"context"
	"errors"
)

// GitVerse may expose workflows while all execution capacity is unavailable.
// Cancellation is platform evidence, never proof of an exhausted quota.
func (c *Client) gitverseRunnerHealth(ctx context.Context) error {
	var report struct {
		Runs         []ciRun `json:"runs"`
		WorkflowRuns []ciRun `json:"workflow_runs"`
	}
	// A sample of the newest runs: the list always has more pages once the
	// repository has run CI ten times, which the evidence reader rejects.
	if err := c.ciFirstPage(ctx, c.ciRoute()+"/actions/runs?per_page=10", &report); err != nil {
		return err
	}
	runs := report.Runs
	if len(runs) == 0 {
		runs = report.WorkflowRuns
	}
	if len(runs) == 0 {
		return nil
	}
	waiting, cancelled, unavailable := false, true, true
	for _, r := range runs {
		pending := r.Status == "waiting" || r.Status == "queued" || r.Status == "pending"
		stopped := r.Status == "cancelled" || r.Status == "canceled"
		waiting = waiting || pending
		cancelled = cancelled && stopped
		unavailable = unavailable && (pending || stopped)
	}
	if unavailable && waiting {
		return errors.New("CI_NO_RUNNER: recent GitVerse runs only wait or cancel without executing; quota unknown")
	}
	if cancelled {
		return errors.New("CI_PLATFORM_CANCELLED: all recent GitVerse runs were cancelled; quota unknown")
	}
	return nil
}
