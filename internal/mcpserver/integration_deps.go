package mcpserver

import (
	"context"
	"mcpshka/internal/evidence"
	"mcpshka/internal/executor"
	"mcpshka/internal/tools/integration"
)

func newIntegrationService() integration.Service {
	return integration.Service{Owner: uploadOwner, AdminOwner: adminOwner, Admin: runAdminSession[map[string]any], Authorize: authorizeTool, Published: func(ctx context.Context, a executor.IntegrationApproval) {
		inputs := evidence.Inputs{Head: a.Request.Commit, Tree: gitRev(ctx, a.Request.Path, a.Request.Commit+"^{tree}")}
		if inputs.Tree != "" {
			recordGate(a.Request.Path, "engineering", true, inputs, "integration_publish_main", a.ExecutionID)
			recordGate(a.Request.Path, "integration", true, inputs, "integration_publish_main", a.ExecutionID)
		}
	}}
}
