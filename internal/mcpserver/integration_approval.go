package mcpserver

import (
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/integration"
)

type IntegrationApprovalInput = integration.IntegrationApprovalInput

var integrationApprovalCall = integration.ApprovalCall

func registerIntegrationApprovalTool(r *toolRegistry, m *jobs.Manager) {
	integration.RegisterApproval(r.install, m)
}
