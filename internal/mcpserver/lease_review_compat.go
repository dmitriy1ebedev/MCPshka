package mcpserver

import (
	"encoding/json"
	"errors"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
)

// Read-only compatibility bridge; no approval or takeover can be requested here.
func leaseReviewCompat(m *jobs.Manager, s *projectlease.Service, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Args) != 2 || len(in.Args[1]) > 4096 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return jobs.Snapshot{}, errors.New("lease-review requires PATH without overrides")
	}
	if _, err := m.Policy().CheckWorkdir(in.Workdir); err != nil {
		return jobs.Snapshot{}, err
	}
	if _, err := m.Policy().DecideCommand("mcpshka-project-context", nil); err != nil {
		return jobs.Snapshot{}, err
	}
	out, err := s.ReviewAdministrativeTransfer(in.Args[1])
	if err != nil {
		return jobs.Snapshot{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	return m.StoreReceipt("mcpshka", []string{"lease-review", out.ProjectRoot}, in.Workdir, string(raw))
}
