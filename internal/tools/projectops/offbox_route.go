package projectops

import (
	"context"

	"mcpshka/internal/forgeci"
	"mcpshka/internal/jobs"
	"mcpshka/internal/offbox"
	"mcpshka/internal/project"
	"mcpshka/internal/runnerhub"
)

// offboxTest: a targeted Go test goes off H96 first (internal/offbox).
func OffboxTest(ctx context.Context, plan *project.TestPlan, packages []string, timeout int, local func() (jobs.Snapshot, error)) (jobs.Snapshot, bool) {
	s, why, ok := offbox.Default.Route(ctx, plan.Root, packages, timeout, local)
	plan.Reason += why
	return s, ok
}

// wireOffbox: sites are woken by forge_ci mode=runner jobs; rj- ids are
// served through the job tools.
func WireOffbox(m *jobs.Manager) {
	offbox.Default.Wake = offbox.JobWaker(func(site string) (string, error) {
		args, opts, err := forgeci.JobInput(site, "runner", "", "offbox-wake-"+runnerhub.NewJobID(), "", "")
		if err != nil {
			return "", err
		}
		s, err := m.StartWithOptions(forgeci.Command, args, forgeci.Root, opts)
		return s.ID, err
	}, m)
	offbox.Default.Resume(m)
}
