package toolcontract

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

func SessionTool(name string) bool {
	return strings.HasPrefix(name, "proc_") || strings.HasPrefix(name, "search_")
}

type mutationContract struct {
	Probe        string `json:"probe"`
	Recovery     string `json:"recovery"`
	Key          string `json:"key"`
	OutputLimit  int    `json:"output_limit_bytes"`
	BeforeAfter  bool   `json:"before_after"`
	Irreversible bool   `json:"irreversible,omitempty"`
	// ReplayPending lets the same retry key re-run a handler whose backend
	// replays its own journaled outcome, so a lost response is not UNKNOWN.
	ReplayPending bool `json:"replay_pending,omitempty"`
}

func (c mutationContract) Validate() error {
	if c.Probe == "" || c.Recovery == "" || c.Key == "" || c.OutputLimit < 1 || c.OutputLimit > 131072 || !c.BeforeAfter {
		return errors.New("mutation contract requires probe, retry key, recovery, bounded output and before/after")
	}
	if c.Irreversible && c.Recovery != "APPROVAL_REQUIRED" {
		return errors.New("irreversible mutation requires approval")
	}
	return nil
}
func contract(probe, recovery, key string) mutationContract {
	return mutationContract{Probe: probe, Recovery: recovery, Key: key, OutputLimit: 131072, BeforeAfter: true, Irreversible: recovery == "APPROVAL_REQUIRED"}
}

// Replay is the contract of a dynamically registered tool (tool_forge
// composites): its own name is the recovery, the retry key is required.
func Replay(name string) Contract { return contract("state_status", name, "idempotency_key") }

func replayable(c mutationContract) mutationContract {
	c.ReplayPending = true
	return c
}

var Contracts = map[string]mutationContract{
	"lane":                              contract("lane_status", "lane", "idempotency_key"),
	"phase_checkpoint_record":           contract("phase_gate", "phase_gate", "idempotency_key"),
	"project_release":                   contract("project_release_status", "project_release_status", "idempotency_key"),
	"roadmap_close":                     contract("roadmap_next", "roadmap_next", "idempotency_key"),
	"issue_create":                      contract("issue_list", "issue_list", "idempotency_key"),
	"issue_update":                      contract("issue_get", "issue_get", "idempotency_key"),
	"issue_accept":                      contract("issue_get", "issue_get", "idempotency_key"),
	"issue_import":                      contract("issue_list", "issue_list", "idempotency_key"),
	"issue_dispatch":                    contract("issue_get", "issue_get", "idempotency_key"),
	"cloud_agent_task":                  contract("state_status", "cloud_task_status", "idempotency_key"),
	"cloud_agent_poll":                  contract("cloud_task_status", "cloud_task_status", "idempotency_key"),
	"docker_smoke":                      replayable(contract("docker_smoke_status", "docker_smoke_status", "idempotency_key")),
	"project_create":                    contract("state_status", "APPROVAL_REQUIRED", "idempotency_key"),
	"media_stream":                      contract("state_status", "media_stream", "idempotency_key"),
	"plugin_call":                       contract("plugin_tools", "plugin_call", "idempotency_key"),
	"plugin_sync":                       contract("plugin_tools", "plugin_sync", "idempotency_key"),
	"webos_deploy":                      replayable(contract("state_status", "approved_task_status", "idempotency_key")),
	"tool_forge":                        contract("state_status", "tool_forge", "idempotency_key"),
	"ha_exposure_sync":                  contract("state_status", "ha_exposure_sync", "idempotency_key"),
	"ha_entity_update":                  contract("state_status", "ha_entity_update", "idempotency_key"),
	"approved_task_prepare":             replayable(contract("approved_task_status", "approved_task_status", "idempotency_key")),
	"approved_task_execute":             contract("approved_task_status", "APPROVAL_REQUIRED", "idempotency_key"),
	"storage_connect":                   contract("storage_auth_status", "storage_auth_status", "idempotency_key"),
	"storage_oauth_app_set":             contract("storage_providers", "storage_providers", "idempotency_key"),
	"storage_auth_accept":               contract("storage_auth_status", "storage_auth_status", "idempotency_key"),
	"storage_mkdir":                     replayable(contract("storage_transfer_status", "storage_transfer_status", "idempotency_key")),
	"storage_upload":                    replayable(contract("storage_transfer_status", "storage_transfer_status", "idempotency_key")),
	"storage_download":                  replayable(contract("storage_transfer_status", "storage_transfer_status", "idempotency_key")),
	"storage_move":                      replayable(contract("storage_transfer_status", "storage_transfer_status", "idempotency_key")),
	"storage_rename":                    replayable(contract("storage_transfer_status", "storage_transfer_status", "idempotency_key")),
	"storage_trash":                     replayable(contract("storage_transfer_status", "storage_transfer_status", "idempotency_key")),
	"backup_create":                     replayable(contract("backup_status", "backup_status", "idempotency_key")),
	"backup_verify":                     replayable(contract("backup_status", "backup_status", "idempotency_key")),
	"knowledge_import_chat":             contract("knowledge_health", "knowledge_health", "idempotency_key"),
	"project_lease_admin_fence_repair":  contract("project_lease_admin_fence_preview", "project_lease_admin_fence_preview", "idempotency_key"),
	"release_build_prepare":             contract("state_status", "state_status", "idempotency_key"),
	"release_prepare":                   contract("job_status", "job_cancel", "idempotency_key"),
	"release_local":                     contract("state_status", "state_status", "idempotency_key"),
	"forge_ci":                          contract("job_status", "job_cancel", "idempotency_key"),
	"git_provider_change":               contract("git_provider_query", "git_provider_query", "idempotency_key"),
	"git_provider_push":                 contract("job_status", "job_cancel", "idempotency_key"),
	"integration_worker_launch":         contract("integration_approval_status", "integration_worker_status", "idempotency_key"),
	"integration_worker_fence":          contract("integration_worker_status", "integration_worker_status", "idempotency_key"),
	"integration_worker_recover":        contract("integration_worker_status", "integration_worker_status", "idempotency_key"),
	"integration_worker_abort":          contract("integration_recovery_status", "integration_recovery_status", "idempotency_key"),
	"executor_admin_stale_reconcile":    replayable(contract("executor_admin_stale_preview", "executor_admin_stale_preview", "idempotency_key")),
	"executor_fence":                    contract("executor_fence_status", "executor_fence_status", "idempotency_key"),
	"integration_enqueue":               contract("integration_status", "integration_status", "idempotency_key"),
	"integration_cancel":                contract("integration_status", "integration_status", "idempotency_key"),
	"integration_recover":               contract("integration_recovery_status", "integration_recovery_status", "idempotency_key"),
	"integration_test_prepare":          contract("state_status", "state_status", "idempotency_key"),
	"integration_approve":               contract("integration_status", "integration_status", "idempotency_key"),
	"integration_publish_main":          replayable(contract("integration_recovery_status", "integration_recovery_status", "idempotency_key")),
	"project_work_yield":                contract("project_lease_status", "project_work_yield_status", "idempotency_key"),
	"artifact_upload_begin":             contract("artifact_upload_status", "artifact_upload_cancel", "idempotency_key"),
	"artifact_upload_chunk":             contract("artifact_upload_status", "artifact_upload_status", "idempotency_key"),
	"artifact_upload_finish":            contract("artifact_upload_status", "artifact_upload_status", "idempotency_key"),
	"artifact_upload_cancel":            contract("artifact_upload_status", "artifact_upload_status", "idempotency_key"),
	"knowledge_write":                   contract("knowledge_health", "knowledge_health", "idempotency_key"),
	"memory_write":                      contract("state_status", "state_status", "idempotency_key"),
	"search_reindex":                    contract("index_health", "index_health", "idempotency_key"),
	"diag_investigation_start":          contract("diag_investigation_status", "diag_investigation_cancel", "idempotency_key"),
	"diag_investigation_cancel":         contract("diag_investigation_status", "diag_investigation_status", "idempotency_key"),
	"workflow_start":                    contract("workflow_status", "workflow_resume", "idempotency_key"),
	"workflow_resume":                   contract("workflow_status", "workflow_resume", "idempotency_key"),
	"rescue_start":                      contract("rescue_status", "rescue_start", "idempotency_key"),
	"network_stage_apply":               contract("network_stage_status", "network_stage_rollback", "idempotency_key"),
	"network_stage_commit":              contract("network_stage_status", "network_stage_rollback", "idempotency_key"),
	"network_stage_rollback":            contract("network_stage_status", "network_stage_rollback", "idempotency_key"),
	"traffic_route_start":               contract("traffic_route_status", "bounded_expiry", "idempotency_key"),
	"traffic_route_cancel":              contract("traffic_route_status", "traffic_route_status", "idempotency_key"),
	"approval_request":                  contract("approval_status", "bounded_expiry", "idempotency_key"),
	"service_action":                    contract("service_status", "service_action", "idempotency_key"),
	"docker_action":                     contract("docker_status", "docker_action", "idempotency_key"),
	"proc_start":                        contract("proc_list", "proc_kill", "idempotency_key"),
	"proc_send":                         contract("proc_list", "proc_kill", "idempotency_key"),
	"proc_kill":                         contract("proc_list", "proc_kill", "session_id"),
	"search_start":                      contract("state_status", "search_stop", "idempotency_key"),
	"search_stop":                       contract("state_status", "search_stop", "id"),
	"fs_move":                           contract("state_status", "fs_stat", "idempotency_key"),
	"fs_mkdir":                          contract("fs_stat", "fs_mkdir", "idempotency_key"),
	"ps_kill":                           contract("ps_list", "ps_list", "idempotency_key"),
	"exec_start":                        contract("job_status", "job_cancel", "idempotency_key"),
	"fixes_night":                       contract("job_status", "fixes_review", "idempotency_key"),
	"fixes_review":                      contract("git_status", "fixes_review", "idempotency_key"),
	"runner_task":                       contract("runner_status", "runner_status", "idempotency_key"),
	"job_cancel":                        contract("job_status", "job_cancel", "job_id"),
	"job_artifact_gc_apply":             contract("job_artifact_gc_preview", "job_artifact_gc_preview", "idempotency_key"),
	"opencore_publish":                  contract("state_status", "opencore_preview", "idempotency_key"),
	"fs_patch":                          contract("fs_read", "fs_patch", "idempotency_key"),
	"fs_write":                          contract("fs_stat", "fs_write", "idempotency_key"),
	"pdf_write":                         contract("fs_stat", "pdf_write", "idempotency_key"),
	"root_task_begin_passkey":           contract("root_task_status", "bounded_expiry", "idempotency_key"),
	"root_task_authorize":               contract("root_task_status", "bounded_expiry", "idempotency_key"),
	"root_task_execute":                 contract("root_task_status", "bounded_expiry", "idempotency_key"),
	"admin_session_begin":               contract("admin_session_status", "admin_session_end", "idempotency_key"),
	"admin_session_end":                 contract("admin_session_status", "admin_session_end", "idempotency_key"),
	"admin_session_authorize_task":      contract("root_task_status", "admin_session_end", "idempotency_key"),
	"admin_session_retire_deployment":   contract("project_deploy_status", "admin_session_end", "idempotency_key"),
	"admin_archive_prepare":             contract("admin_archive_status", "admin_archive_status", "idempotency_key"),
	"admin_archive_execute":             contract("admin_archive_status", "admin_archive_status", "idempotency_key"),
	"admin_archive_reconcile":           contract("admin_archive_status", "admin_archive_status", "idempotency_key"),
	"project_lease_admin_recover":       replayable(contract("project_lease_admin_recovery_status", "project_lease_admin_recovery_status", "idempotency_key")),
	"project_run":                       contract("job_status", "job_cancel", "idempotency_key"),
	"project_onboard":                   contract("project_onboard_preview", "project_onboard", "idempotency_key"),
	"ha_service_call":                   contract("ha_check_config", "ha_service_call", "idempotency_key"),
	"roadmap_run":                       contract("roadmap_run_status", "roadmap_run_status", "idempotency_key"),
	"agent_task":                        contract("state_status", "agent_task_status", "idempotency_key"),
	"brain_ask":                         contract("state_status", "brain_status", "idempotency_key"),
	"brain_cancel":                      contract("state_status", "brain_status", "idempotency_key"),
	"agent_task_cancel":                 contract("state_status", "agent_task_status", "idempotency_key"),
	"diag_release_apply":                contract("diag_release_status", "diag_release_status", "idempotency_key"),
	"release_apply":                     contract("release_apply_status", "release_apply_status", "idempotency_key"),
	"project_repo_adopt":                contract("project_repo_preview", "project_repo_preview", "idempotency_key"),
	"browser_run":                       contract("job_status", "job_cancel", "idempotency_key"),
	"doc_inspect":                       contract("job_status", "job_cancel", "idempotency_key"),
	"doc_edit":                          contract("job_status", "job_cancel", "idempotency_key"),
	"project_commit":                    contract("project_lease_status", "project_lease_status", "idempotency_key"),
	"project_worktree":                  contract("git_status", "project_worktree", "idempotency_key"),
	"project_git_unlock":                contract("project_lease_status", "project_lease_status", "idempotency_key"),
	"project_lease_admin_stale_release": replayable(contract("project_lease_status", "project_lease_status", "idempotency_key")),
	"workflow_checkpoint_start":         contract("workflow_checkpoint_status", "workflow_checkpoint_start", "idempotency_key"),
	"project_test":                      contract("job_status", "job_cancel", "idempotency_key"),
	"project_pipeline":                  contract("job_status", "job_cancel", "idempotency_key"),
	"mission_start":                     replayable(contract("mission_status", "mission_cancel", "idempotency_key")),
	"mission_tick":                      contract("mission_status", "mission_pause", "idempotency_key"),
	"mission_resume":                    contract("mission_status", "mission_pause", "idempotency_key"),
	"mission_pause":                     contract("mission_status", "mission_resume", "idempotency_key"),
	"mission_cancel":                    contract("mission_status", "mission_status", "idempotency_key"),
	"project_build":                     contract("job_status", "job_cancel", "idempotency_key"),
	"project_lease_resume":              replayable(contract("project_lease_status", "project_lease_status", "idempotency_key")),
	"project_lease_acquire":             replayable(contract("project_lease_status", "project_lease_release", "idempotency_key")),
	"project_lease_release":             contract("project_lease_status", "project_lease_acquire", "idempotency_key"),
	"project_lease_heartbeat":           contract("project_lease_status", "project_lease_release", "idempotency_key"),
	"project_deploy":                    contract("project_deploy_status", "project_deploy", "idempotency_key"),
	"broker_install":                    contract("control_path_status", "APPROVAL_REQUIRED", "idempotency_key"),
	"docker_terminal_open":              contract("docker_terminal_status", "APPROVAL_REQUIRED", "idempotency_key"),
	"docker_terminal_io":                contract("docker_terminal_status", "bounded_expiry", "idempotency_key"),
	"service_user_migrate":              contract("control_path_status", "APPROVAL_REQUIRED", "idempotency_key"),
	"project_artifact_cleanup":          contract("git_status", "project_artifact_cleanup", "idempotency_key"),
	"project_cleanup_candidates":        contract("state_status", "APPROVAL_REQUIRED", "idempotency_key"),
	"codex_task_submit":                 contract("codex_task_status", "codex_task_cancel", "idempotency_key"),
	"codex_session_create":              contract("codex_session_creation_status", "codex_session_creation_status", "idempotency_key"),
	"codex_session_load":                contract("codex_session_creation_status", "codex_session_creation_status", "idempotency_key"),
	"codex_task_cancel":                 contract("codex_task_status", "codex_task_cancel", "session_id+queue_id"),
	"codex_intent_create":               contract("codex_intent_status", "codex_intent_cancel", "idempotency_key"),
	"codex_intent_update":               contract("codex_intent_status", "codex_intent_cancel", "idempotency_key"),
	"codex_intent_cancel":               contract("codex_intent_status", "codex_intent_cancel", "idempotency_key"),
	"state_backup":                      contract("state_backup_status", "APPROVAL_REQUIRED", "idempotency_key"),
	"state_restore_verify":              contract("state_backup_status", "state_restore_verify", "idempotency_key"),
	"mutation_approval_request":         contract("approval_status", "bounded_expiry", "idempotency_key"),
}

func RetryKey(c mutationContract, in map[string]any) (string, error) {
	parts := strings.Split(c.Key, "+")
	values := make([]string, 0, len(parts))
	for _, field := range parts {
		v, ok := in[field].(string)
		if !ok || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("%s is required by mutation contract", field)
		}
		values = append(values, v)
	}
	key := strings.Join(values, "\n")
	if len(key) > 256 || strings.ContainsRune(key, 0) {
		return "", errors.New("mutation retry key exceeds 256 bytes")
	}
	return key, nil
}

func RequireInputKey(name string, c mutationContract, typ reflect.Type) {
	if typ.Kind() != reflect.Struct {
		panic("mutation input must be a struct: " + name)
	}
	for _, key := range strings.Split(c.Key, "+") {
		found := false
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if strings.Split(f.Tag.Get("json"), ",")[0] == key && f.Type.Kind() == reflect.String {
				found = true
				break
			}
		}
		if !found {
			panic("mutation input lacks contract key: " + name + "/" + key)
		}
	}
}

type Contract = mutationContract
