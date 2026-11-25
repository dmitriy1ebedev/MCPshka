package mcpserver

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/failure"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/mission"
)

func registerMissionControllerTools(r *toolRegistry, c *mission.Controller, store *idempotency.Store) {
	addTool(r, &mcp.Tool{Name: "mission_preflight", Description: "Check each explicit mission step with capability_preflight and acceptance_status at a clean HEAD/tree. No execution; blockers include next_safe_action.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in mission.Plan) (*mcp.CallToolResult, mission.Preflight, error) {
		out, err := c.Preflight(ctx, in)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "mission_start", Description: "Persist a bounded mission independently of chat. Optional workflow_plan starts its exact trusted native pipeline atomically with the mission and writer claim, requiring workflow_start permission; background maintenance advances it. Optional workflow_id only binds existing work. Agents are never launched by mission intake. Pipeline success alone cannot prove production acceptance.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in mission.StartInput) (*mcp.CallToolResult, mission.Snapshot, error) {
		if in.WorkflowPlan != nil {
			if err := authorizeTool(ctx, &mcp.Tool{Name: "workflow_start", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}); err != nil {
				return nil, mission.Snapshot{}, failure.Rejected("MISSION_WORKFLOW_UNAUTHORIZED", "mission.start", "workflow_start permission required before mission dispatch", err)
			}
		}
		// The controller's original durable mission ID reconciles a lost reply.
		out, err := c.Start(ctx, in)
		return nil, out, err
	})
	for _, name := range []string{"mission_resume", "mission_pause", "mission_cancel", "mission_tick"} {
		addTool(r, &mcp.Tool{Name: name, Description: "Update durable mission state. Resume revalidates acceptance at current HEAD/tree and advances the complete proven checkpoint prefix. For a bound workflow, pause stops new stage admission; cancel reconciles the original admitted job and uses only the pinned rollback/finalizer. The current admitted job is not forcibly killed. Controls and deadlines survive restart; cancelled/expired missions cannot resume. A new key is required for each new transition.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in mission.ControlInput) (*mcp.CallToolResult, mission.Snapshot, error) {
			out, err := runIdempotent(store, in.IdempotencyKey, name, in, func() (mission.Snapshot, error) {
				switch name {
				case "mission_pause":
					return c.Pause(in)
				case "mission_cancel":
					return c.Cancel(in)
				case "mission_tick":
					return c.Tick(ctx, in)
				default:
					return c.Resume(ctx, in)
				}
			})
			return nil, out, err
		})
	}
	addTool(r, &mcp.Tool{Name: "mission_report", Description: "Read current Mission, Current step, State, Version, Git HEAD, Passed gates, Pending, Production, Active job, Next action, User action required, who holds the project lease now (held by another chat/agent → wait or work read-only, do not fight for it) and the last 5 commits. Missing production evidence stays UNKNOWN.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in mission.StatusInput) (*mcp.CallToolResult, mission.Summary, error) {
		out, err := c.Report(ctx, in.ID)
		return nil, out, err
	})
}
