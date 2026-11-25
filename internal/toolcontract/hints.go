package toolcontract

// Annotation hints for MCP clients (MCP-185): MCP defaults an unset
// destructiveHint and openWorldHint to true, and ChatGPT stops such calls
// before they reach MCPshka. mcpserver fills unset hints from these tables.

// openWorld tools reach services outside H96.
var openWorldTools = map[string]bool{
	"fixes_night": true,
	"agent_task":  true, "browser_run": true, "browser_screenshot": true, "cloud_agent_task": true, "cloud_agent_poll": true,
	"cloud_agent_observe": true, "cloud_fanin": true, "forge_ci": true, "git_provider_change": true, "git_provider_push": true,
	"integration_publish_main": true, "integration_ship": true, "runner_task": true, "storage_backup": true, "storage_connect": true,
	"storage_upload": true, "storage_download": true, "storage_move": true, "storage_mkdir": true, "storage_trash": true,
	"storage_oauth_app_set": true, "knowledge_import_chat": true,
}

// additive tools only add commits, records, test runs or leases: nothing an
// owner loses (an idempotency key makes a retry the same call).
var additiveTools = map[string]bool{
	"fixes_night": true, "fixes_review": true,
	"project_commit": true, "project_test": true, "project_build": true, "project_worktree": true,
	"project_lease_acquire": true, "project_lease_heartbeat": true, "project_lease_release": true, "project_lease_resume": true,
	"phase_checkpoint_record": true, "roadmap_close": true, "issue_create": true, "issue_update": true, "issue_accept": true,
	"memory_write": true, "knowledge_write": true, "job_wait": true, "workflow_start": true, "mission_tick": true,
}

// OpenWorld reports a tool that reaches services outside H96.
func OpenWorld(name string) bool { return openWorldTools[name] }

// Additive reports a tool that only adds history (commits, records, runs).
func Additive(name string) bool { return additiveTools[name] }

// AdditiveTools lists them, for tests.
func AdditiveTools() []string {
	out := make([]string, 0, len(additiveTools))
	for n := range additiveTools {
		out = append(out, n)
	}
	return out
}
