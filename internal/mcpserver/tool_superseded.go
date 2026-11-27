package mcpserver

// supersededTools are duplicates kept callable for clients with a cached
// catalog; tool_resolve no longer offers them and nothing may route to them
// (TestToolCatalogLint). Remove one once tool_usage shows no calls: 0.11.3
// removed docker_restart, service_restart and service_logs (MCP-163; use
// docker_action/service_action action=restart and journal_read).
// 0.11.8 (tool audit, owner 12.11.2026): duplicates are not deleted, they
// go here — «копилка»: out of tools/list, callable by name, recorded in
// memory («копилка инструментов») so nobody writes them again.
var supersededTools = map[string]string{"fs_read_many": "fs_read", "idempotency_status": "mutation_probe", "knowledge_read": "memory_recall", "project_detect": "project_context",
	// 0.12.0: the storage_connect page takes app keys and tokens in the browser; rename is the same operation as move.
	"storage_oauth_app_set": "storage_connect", "storage_auth_accept": "storage_connect", "storage_rename": "storage_move"}

// listedTools is the stable default catalog: 34 busiest tools from the
// lifetime tool_usage ledger on 2026-11-17, plus discovery and telemetry.
// Others stay callable through tool_resolve/tool_invoke/code_run.
// HTTP ?tools=all or MCPSHKA_TOOL_CATALOG=all restores the complete catalog.
var listedTools = map[string]bool{
	"capabilities_bootstrap": true, "tool_resolve": true, "tool_invoke": true, "code_run": true,
	"mcpshka_guide": true, "tool_usage": true, "job_wait": true, "fs_read": true,
	"fs_patch": true, "job_output": true, "forge_ci": true, "project_lease_heartbeat": true,
	"exec_start": true, "admin_session_status": true, "fs_write": true, "integration_status": true,
	"project_test": true, "project_lease_acquire": true, "integration_test_prepare": true, "project_commit": true,
	"release_local": true, "project_context": true, "fs_find": true, "integration_worker_launch": true,
	"job_status": true, "diag_release_status": true, "git_status": true, "release_apply_status": true,
	"integration_worker_status": true, "project_lease_status": true, "fs_list": true, "issue_get": true,
	"agent_task_status": true, "project_lease_release": true, "ha_state": true, "exec_classify": true,
	"issue_update": true, "issue_list": true, "phase_gate": true, "approved_task_status": true,
}

// hiddenTool reports a tool kept out of tools/list (superseded or not listed).
func hiddenTool(name string) bool {
	if _, ok := supersededTools[name]; ok {
		return true
	}
	return !listedTools[name]
}

func supersededNote(name string) string {
	if to, ok := supersededTools[name]; ok {
		return "Устарело (дубль): используйте " + to + ". "
	}
	return ""
}
