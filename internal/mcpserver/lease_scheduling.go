package mcpserver

import (
	"strings"

	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/workflow"
)

func leaseSchedulingStatus(s projectlease.Status, a jobs.ProjectActivity, claim *workflow.Run, err error) ProjectLeaseRecoveryStatus {
	out := leaseRecoveryStatus(s, a)
	if err != nil {
		out.WriterScopeState = "unknown"
		out.NextAction = "reconcile_durable_authority_before_writing"
		out.RecoveryBlockers = append(out.RecoveryBlockers, "workflow_authority_unverified")
		return out
	}
	if claim != nil {
		out.WriterScopeState = "busy"
		out.NextAction = "reconcile_existing_workflow_or_use_independent_scope"
		out.RecoveryBlockers = append(out.RecoveryBlockers, "workflow_claim_pending")
	}
	return out
}

// markHolderGone names the recovery path for a lease whose holder can no
// longer renew or release it: a workflow lease whose owner job record is gone,
// or a roadmap-run lease whose run has stopped (it keeps the lease on purpose
// for reconciliation). Waiting for the holder never ends there
// (BUG-20261103-9); release still needs the Face ID stale path.
func markHolderGone(out ProjectLeaseRecoveryStatus, s projectlease.Status, a jobs.ProjectActivity, claim *workflow.Run, gone, reason string) ProjectLeaseRecoveryStatus {
	if !s.Held || gone == "" || a.Count > 0 || claim != nil {
		return out
	}
	out.WriterScopeState = "orphaned"
	out.NextAction = "project_lease_admin_stale_preview_then_stale_release"
	if reason != "" {
		out.NextAction += " (" + reason + ")"
	}
	out.RecoveryBlockers = append(out.RecoveryBlockers, gone)
	return out
}

// leaseHolderGone reports why the lease holder is provably not running, or "".
func leaseHolderGone(s projectlease.Status, jobMissing func(string) bool, runState func(string) string) (string, string) {
	switch {
	case strings.HasPrefix(s.OwnerJobID, "workflow-") && jobMissing(s.OwnerJobID):
		return "owner_job_record_missing", ""
	case strings.HasPrefix(s.Owner, "roadmap-run:"):
		id := strings.TrimPrefix(s.Owner, "roadmap-run:")
		if st := runState(id); st == "stopped" || st == "done" || st == "interrupted" {
			return "roadmap_run_" + st, "reconcile roadmap_run_status id=" + id + " first; WIP is kept"
		}
	}
	return "", ""
}

// validRoadmapRunID keeps a lease owner name from addressing another file.
func validRoadmapRunID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, "/\\") && !strings.HasPrefix(id, ".")
}
