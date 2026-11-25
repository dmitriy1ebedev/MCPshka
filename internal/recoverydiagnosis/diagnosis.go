// Package recoverydiagnosis classifies bounded observations into safe recovery
// guidance. Log text is untrusted evidence, never execution authority.
package recoverydiagnosis

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const MaxLogBytes = 8192

type Evidence struct {
	Authenticated, LegacyOwnerUnbound, HistoricalDeployUnknown, BrokerIncompatible                                                 bool
	JobKnown, LeaseHeld, OwnerVerified, StaleHeartbeat, DeploymentUnverified, CheckpointIncomplete, ResourceLimited, WorkflowClaim bool
	JobState, Log                                                                                                                  string
}
type Finding struct {
	Fingerprint       string   `json:"fingerprint"`
	SafeParallelScope string   `json:"safe_parallel_scope,omitempty"`
	DoNot             []string `json:"do_not,omitempty"`
	Code              string   `json:"code"`
	Verification      string   `json:"verification"`
	Recovery          []string `json:"recovery"`
}
type Result struct {
	Findings          []Finding `json:"findings"`
	RetryAllowed      bool      `json:"retry_allowed"`
	EvidenceTruncated bool      `json:"evidence_truncated"`
}

func Diagnose(e Evidence) Result {
	out := Result{Findings: []Finding{}}
	add := func(code, verification string, steps ...string) {
		for _, f := range out.Findings {
			if f.Code == code {
				return
			}
		}
		sum := sha256.Sum256([]byte(code))
		scope := ""
		if code == "AUTH_OK_but_LEASE_BUSY" || code == "legacy_owner_unbound" {
			scope = "independent_sibling_worktree"
		}
		out.Findings = append(out.Findings, Finding{Fingerprint: hex.EncodeToString(sum[:]), SafeParallelScope: scope, Code: code, Verification: verification, Recovery: steps, DoNot: prohibited(code)})
	}
	if e.Authenticated && e.LeaseHeld {
		add("AUTH_OK_but_LEASE_BUSY", "observed", "authentication_is_not_writer_ownership", "use_own_holder_token_or_independent_scope", "wait_for_verified_holder_release")
	}
	if e.LegacyOwnerUnbound {
		add("legacy_owner_unbound", "observed", "verify_original_owner_session_and_process_binding", "retain_hold_until_authorized_fenced_transfer", "use_independent_scope")
	}
	if e.HistoricalDeployUnknown {
		add("historical_deploy_unknown", "observed", "probe_original_deployment", "verify_production_identity_and_rollback", "preserve_nonreplay_fence")
	}
	if e.BrokerIncompatible {
		add("broker_incompatible", "observed", "compare_live_broker_and_controller_versions", "use_pinned_paired_release_workflow", "retain_protected_core_gates")
	}
	if !e.JobKnown || e.JobState == "unknown" {
		add("JOB_UNKNOWN", "observed", "probe_original_operation", "preserve_checkpoint", "do_not_repeat_unknown_effect")
	}
	if e.JobKnown && e.JobState == "failed" {
		add("JOB_FAILED", "observed", "read_bounded_original_output", "correct_demonstrated_cause", "verify_result_before_checkpoint")
	}
	log := e.Log
	if len(log) > MaxLogBytes {
		log = log[:MaxLogBytes]
		out.EvidenceTruncated = true
	}
	if e.JobKnown && (e.JobState == "failed" || e.JobState == "interaction_failed") {
		log = strings.ToLower(log)
		inferred := "inferred_from_untrusted_output"
		if strings.Contains(log, "auth_ok") && strings.Contains(log, "lease_busy") {
			add("AUTH_OK_but_LEASE_BUSY", inferred, "verify_authenticated_identity_and_live_lease_separately", "use_independent_scope")
		}
		if strings.Contains(log, "legacy_owner_unbound") {
			add("legacy_owner_unbound", inferred, "probe_original_lease_owner_binding", "retain_hold_until_authorized_fenced_transfer", "use_independent_scope")
		}
		if strings.Contains(log, "historical_deploy_unknown") {
			add("historical_deploy_unknown", inferred, "probe_original_deployment", "verify_production_identity_and_rollback", "preserve_nonreplay_fence")
		}
		if strings.Contains(log, "broker_incompatible") || strings.Contains(log, "broker incompatible") {
			add("broker_incompatible", inferred, "compare_live_broker_and_controller_versions", "use_pinned_paired_release_workflow", "retain_protected_core_gates")
		}
		if strings.Contains(log, "permission denied") || strings.Contains(log, "access denied") {
			add("PERMISSION_DENIED", inferred, "inspect_allowed_path_and_permissions", "use_private_test_fixture", "do_not_read_production_secrets")
		}
		if strings.Contains(log, "config") && (strings.Contains(log, "no such file") || strings.Contains(log, "config missing") || strings.Contains(log, "missing config")) {
			add("MISSING_CONFIG", inferred, "verify_test_config_exists_in_allowed_scope", "create_private_test_fixture", "retry_only_known_failed_test_with_fresh_key")
		}
		if strings.Contains(log, "quota exhausted") || strings.Contains(log, "quota exceeded") || strings.Contains(log, "usage limit") || strings.Contains(log, "provider_quota_exhausted") {
			add("PROVIDER_QUOTA_EXHAUSTED", inferred, "check_confirmed_provider_status", "save_durable_checkpoint", "handoff_only_to_authorized_executor", "do_not_bypass_provider_limit")
		}
		if strings.Contains(log, "mcp disconnected") || strings.Contains(log, "mcp_disconnected") {
			add("MCP_DISCONNECTED", inferred, "reconnect_read_only", "probe_known_operation_ids", "do_not_repeat_unknown_effect")
		}
		if strings.Contains(log, "memory limit exceeded") || strings.Contains(log, "cpu time limit") || strings.Contains(log, "resource limit") {
			add("RESOURCE_LIMIT", inferred, "inspect_confirmed_resource_receipt", "reduce_bounded_workload", "do_not_repeat_unchanged_failed_job")
		}
		if strings.Contains(log, "tool schema mismatch") || strings.Contains(log, "schema validation") {
			add("TOOL_SCHEMA_MISMATCH", inferred, "inspect_live_tool_schema", "use_supported_typed_compatibility_route")
		}
	}
	if e.LeaseHeld {
		add("LEASE_CONFLICT", "observed", "verify_own_holder_credentials_or_use_independent_scope", "never_infer_owner_death_from_report")
	}
	if e.LeaseHeld && !e.OwnerVerified {
		add("OWNER_UNVERIFIED", "observed", "verify_session_execution_and_process_identity", "retain_hold_for_unbound_owner")
	}
	if e.LeaseHeld && e.StaleHeartbeat {
		add("STALE_HEARTBEAT", "observed", "probe_owner_and_operations", "never_release_on_age_alone")
	}
	if e.DeploymentUnverified {
		add("DEPLOYMENT_UNVERIFIED", "observed", "probe_original_deployment", "verify_production_and_rollback", "retain_hold")
	}
	if e.WorkflowClaim {
		add("WORKFLOW_ACTIVE", "observed", "probe_existing_workflow_claim", "absence_of_jobs_does_not_prove_completion", "retain_hold")
	}
	if e.CheckpointIncomplete {
		add("CHECKPOINT_INCOMPLETE", "observed", "inspect_durable_checkpoint_and_workflow_claim", "preserve_unknown_work", "record_next_safe_action")
	}
	if e.ResourceLimited {
		found := false
		for _, f := range out.Findings {
			found = found || f.Code == "RESOURCE_LIMIT"
		}
		if !found {
			add("RESOURCE_LIMIT", "observed", "inspect_confirmed_resource_receipt", "reduce_bounded_workload")
		}
	}
	return out
}
