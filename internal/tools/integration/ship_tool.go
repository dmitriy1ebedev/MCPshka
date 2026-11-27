package integration

import "github.com/modelcontextprotocol/go-sdk/mcp"

func ShipTool() *mcp.Tool {
	closed := false
	return &mcp.Tool{
		Name:        "integration_ship",
		Description: "Move one commit through the whole integration conveyor with repeated calls: each call performs the next safe step (enqueue, sealed test prepare/launch/fence, then — after the explicit integration_approve review, whose approval_id you pass back — worker, recover, publish main) and returns stage, every ID and next_action. Same gates as calling those tools directly; retry keys are derived from commit and step, so repeat the same call after a lost reply, restart or wait. Never merges, rebases or deploys.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed},
	}
}
