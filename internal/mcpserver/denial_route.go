package mcpserver

import "strings"

// MCP-043: a policy refusal is never a bare POLICY_DENIED. It names the risk
// the rule protects against and the exact permitted route that still reaches
// the goal (read-only first, then the bounded mutating path, then Face ID).
type denialRoute struct {
	topic string
	marks []string
	risk  string
	route []string
}

var denialRoutes = []denialRoute{
	{"root_command", []string{"root shell", "shell", "sudo", "arbitrary command", "exec_start"},
		"an unbounded root command can change any host state without CAS, receipt or rollback",
		[]string{"exec_classify (shows the allowed class of the command)", "project_run/project_test for project work", "approved_task_prepare -> Face ID -> approved_task_execute for an exact root plan"}},
	{"network", []string{"network", "firewall", "nft", "iptables", "vpn", "route", "dns", "wireguard"},
		"a network change can cut H96 and the owner's access off",
		[]string{"network_snapshot / network_connectivity (read-only)", "network_stage_apply (auto-rollback timer) -> network_stage_commit"}},
	{"protected_service", []string{"protected", "systemd", "unit", "service"},
		"stopping a protected unit can take Home Assistant, Zigbee, MQTT or DNS down",
		[]string{"service_status / journal_read (read-only)", "service_action action=restart for an allowlisted unit", "approved_task_prepare for any other unit (Face ID)"}},
	{"container", []string{"docker", "container"},
		"container mutations can break neighbouring services sharing the host",
		[]string{"docker_list / docker_logs / docker_inspect (read-only)", "docker_action (action=restart) for an allowlisted container"}},
	{"filesystem", []string{"outside", "not under", "allowlist", "allowed root", "read-only root", "path"},
		"a write outside the workspace allowlist could overwrite system or state files",
		[]string{"fs_read / fs_list / fs_find (read-only everywhere permitted)", "fs_patch with expected_sha256 inside an allowlisted root (CAS + backup)", "root_task_prepare for a system file (Face ID)"}},
	{"scope", []string{"lacks scope", "cannot call mutating", "not permitted"},
		"the caller's token or session was not granted this class of action",
		[]string{"capabilities_bootstrap (what this client may call)", "admin_session_prepare -> Face ID for an owner-only action"}},
}

var defaultDenialRoute = denialRoute{"unclassified", nil,
	"the operation is outside the caller's granted scope",
	[]string{"capabilities_bootstrap", "tool_resolve goal=<the task in plain words>"}}

// routeForDenial picks the most specific topic of a refusal text.
func routeForDenial(text string) denialRoute {
	text = strings.ToLower(text)
	for _, r := range denialRoutes {
		for _, m := range r.marks {
			if containsMark(text, m) {
				return r
			}
		}
	}
	return defaultDenialRoute
}

func (r denialRoute) suffix() string {
	return "; denial_topic=" + r.topic + "; risk: " + r.risk + "; allowed_route: " + strings.Join(r.route, " | ")
}
