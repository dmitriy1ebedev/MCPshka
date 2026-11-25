package mcpserver

import (
	"encoding/json"
	"errors"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
)

func leaseHeartbeatCompat(m *jobs.Manager, s *projectlease.Service, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Args) != 2 || len(in.Args[1]) > 4096 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return jobs.Snapshot{}, errors.New("lease heartbeat requires bounded typed input without overrides")
	}
	if _, e := m.Policy().CheckWorkdir(in.Workdir); e != nil {
		return jobs.Snapshot{}, e
	}
	if _, e := m.Policy().DecideCommand("mcpshka-lease-heartbeat", nil); e != nil {
		return jobs.Snapshot{}, e
	}
	var p ProjectLeaseHeartbeatInput
	if e := json.Unmarshal([]byte(in.Args[1]), &p); e != nil {
		return jobs.Snapshot{}, errors.New("invalid heartbeat input")
	}
	status, e := s.HeartbeatRefresh(p.Path, p.Token)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	raw, e := json.Marshal(status)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	return m.StoreReceipt("mcpshka", []string{"lease-heartbeat", status.ProjectRoot}, in.Workdir, string(raw))
}
