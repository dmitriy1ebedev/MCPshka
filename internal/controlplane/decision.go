package controlplane

import "strings"

func decide(in Preflight, decision string, allowed bool, reason string, requirements ...string) Preflight {
	in.Decision = EscalateDecision(in.Decision, decision)
	in.Allowed = allowed && decisionAllowsExecution(in.Decision)
	in.Reason = reason
	in.Requirements = requirements
	in.Notice = nil
	switch {
	case in.Operation == "recovery":
		severity := "info"
		if in.Recovery.CurrentHealth != "healthy" {
			severity = "warning"
		}
		in.Notice = &Notice{
			Type:     "recovery_notice",
			Severity: severity,
			DedupKey: "recovery:" + in.Recovery.CurrentHealth,
			Message:  reason,
		}
	case in.Decision == DecisionApprovalRequired:
		in.Notice = &Notice{
			Type:     "approval_request",
			Severity: "warning",
			DedupKey: "approval:" + in.Operation + ":" + strings.Join(in.BlastRadius.AffectedPaths, ","),
			Message:  reason,
		}
	}
	return in
}

// nextBoot decides a Lane change that only acts after a reboot (MCP-103):
// it touches no live path, but the reboot itself may lose the primary, so a
// verified reserve (Tailscale SSH) is what makes it safe.
func nextBoot(out Preflight, req MutationRequest, blast BlastRadius) Preflight {
	switch {
	case !blast.VerifiedBackupExists:
		return decide(out, DecisionApprovalRequired, false, "next-boot Lane change: a reboot may lose the primary path and no reserve management path is verified",
			"verify an independent management path (control_path_status: Tailscale SSH management_ready and verified), then repeat mutation_preflight")
	case !req.Staged || req.RollbackSeconds < 30 || req.RollbackSeconds > 900:
		return decide(out, DecisionApprovalRequired, false, "next-boot Lane change requires staged application with bounded timed rollback",
			"set staged=true", "set rollback_seconds between 30 and 900")
	}
	return decide(out, DecisionGuarded, true, "next-boot Lane change: no live blast radius, verified reserve path covers the reboot, rollback armed",
		"verify management through the reserve path after the reboot")
}
