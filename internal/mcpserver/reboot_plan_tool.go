package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/jobs"
	"mcpshka/internal/rebootplan"
)

type HostRebootPlanInput struct {
	CheckpointFile string `json:"checkpoint_file,omitempty" jsonschema:"recovery checkpoint written before the reboot"`
}

// MCP-111: read-only reboot preflight; the reboot itself is the returned
// command run through approved_task_prepare (one Face ID per plan).
func registerHostRebootPlan(r *toolRegistry, m *jobs.Manager) {
	closed := false
	addTool(r, &mcp.Tool{
		Name:        "host_reboot_plan",
		Description: "Plan a host reboot (перезагрузить хост): preflight blockers (management units ssh/tailscaled/mcpshka/broker not enabled, running jobs), warnings (missing firewall/ipset persistence, active leases, no checkpoint), the exact delayed reboot command to run via approved_task_prepare (Face ID) and the post-boot checks to verify. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HostRebootPlanInput) (*mcp.CallToolResult, rebootplan.Plan, error) {
		active := m.RegistrySummary(50).Count
		f := rebootplan.Gather(ctx, active, 0)
		f.CheckpointFile = in.CheckpointFile
		return nil, rebootplan.Build(f, time.Now()), nil
	})
}
