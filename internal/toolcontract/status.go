package toolcontract

import (
	"mcpshka/internal/controlplane"
	"mcpshka/internal/jobs"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/networkops"
	"mcpshka/internal/projectlease"
)

func MakeNetworkStageStatus(control controlplane.Snapshot, lane laneprovider.Status, laneErr error, network networkops.StateDigest, networkErr error) NetworkStageStatusResult {
	out := NetworkStageStatusResult{Control: control, LANEState: "unknown", NetworkState: "unknown"}
	if laneErr == nil {
		out.LANE = &lane
		out.LANEState = "observed"
	} else {
		out.Partial = true
		out.Warnings = append(out.Warnings, "LANE status unavailable; network mutation remains gated")
	}
	if networkErr == nil {
		out.Network = &network
		out.NetworkState = "observed"
	} else {
		out.Partial = true
		out.Warnings = append(out.Warnings, "local network snapshot unavailable; no network state inferred")
	}
	return out
}

func LeaseRecoveryStatus(status projectlease.Status, activity jobs.ProjectActivity) ProjectLeaseRecoveryStatus {
	out := ProjectLeaseRecoveryStatus{Status: status, Activity: activity, RecoveryBlockers: []string{}}
	out.Reclaimable = false
	out.WriterScopeState = "lease_free"
	out.ParallelWork = "independent_project_or_sibling_worktree"
	out.NextAction = "probe_workflow_claim_and_deploy_then_acquire_this_scope"
	if status.Held {
		out.WriterScopeState = "busy"
		out.NextAction = "use_independent_scope_or_wait_for_holder_release"
		if status.OwnerJobID != "" {
			out.RecoveryBlockers = append(out.RecoveryBlockers, "managed_owner_requires_live_verification")
		} else {
			out.RecoveryBlockers = append(out.RecoveryBlockers, "owner_identity_unverified", "deployment_state_unverified")
			if status.OwnerPID <= 0 || status.OwnerStartTicks == 0 || status.OwnerBootID == "" {
				out.RecoveryBlockers = append(out.RecoveryBlockers, "pid_start_time_binding_missing")
			}
		}
		if status.ExecutionID == "" {
			out.RecoveryBlockers = append(out.RecoveryBlockers, "legacy_lease_untracked")
		}
	}
	if activity.Count > 0 {
		out.WriterScopeState = "busy"
		out.NextAction = "wait_for_project_jobs_or_use_independent_scope"
		out.RecoveryBlockers = append(out.RecoveryBlockers, "project_jobs_pending")
	}
	return out
}
