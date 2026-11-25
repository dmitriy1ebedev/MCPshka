// Package runtimehealth reads existing jobs, workflow and deployment authority.
package runtimehealth

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"mcpshka/internal/deploy"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/jobs"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/workflow"
)

// Read performs no host or provider probes and never repairs recorded outcomes.
func Read(ctx context.Context, m *jobs.Manager) (healthlayer.Sample, error) {
	if m.Database() == nil {
		return healthlayer.Sample{}, errors.New("SQLite authority unavailable")
	}
	a := m.RegistrySummary(16)
	claim, err := workflow.New(m).ProjectClaimContext(ctx, deploy.DefaultRepoRoot)
	if err != nil {
		return healthlayer.Sample{}, err
	}
	state := "ok"
	if a.UnknownCount > 0 || a.Truncated {
		state = "unknown"
	}
	data := map[string]any{"pending_jobs": a.Count, "unknown_jobs": a.UnknownCount, "truncated": a.Truncated, "workflow_scope": deploy.DefaultRepoRoot}
	if claim != nil {
		data["workflow_id"], data["workflow_state"] = claim.ID, claim.State
	}
	if room, err := roadmapscan.Headroom(filepath.Join(deploy.DefaultRepoRoot, "ROADMAP.md")); err == nil {
		data["roadmap"] = room
		if roadmapBlocksWorkflow(room) && state == "ok" {
			state = "degraded"
		}
	}
	r, err := deploy.New(m.Policy(), filepath.Join(filepath.Dir(m.StateDir()), "deploys")).Reconcile()
	d := map[string]any{"state": "unknown", "source": "deployment_registry"}
	if err == nil {
		d["state"] = "ok"
		d["pending"] = r.Pending
		d["nonterminal_count"], d["retired_unknown_count"] = r.NonterminalCount, r.RetiredCount
		d["reconciliation_required"], d["truncated"] = r.ReconciliationRequired, r.Truncated
		if r.ReconciliationRequired || r.Truncated {
			d["state"] = "unknown"
		}
		// A retired fence proves non-replay, never a successful past outcome.
		if r.RetiredCount > 0 {
			d["historical_outcome"] = "UNKNOWN"
		}
	}
	data["deployments"] = d
	if d["state"] == "unknown" && state == "ok" {
		state = "unknown"
	}
	raw, err := json.Marshal(data)
	return healthlayer.Sample{State: state, Data: raw}, err
}

// roadmapBlocksWorkflow: the 90% warning is advice for the next release notes.
// Gating exec_/job_/roadmap_run on it blocked every executor at 95% with no
// job fault (r20 MCP-046/049); only an unloadable roadmap stops the workflow.
func roadmapBlocksWorkflow(room roadmapscan.Room) bool { return room.Bytes > room.Limit }
