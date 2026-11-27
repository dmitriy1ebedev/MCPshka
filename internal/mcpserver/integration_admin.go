package mcpserver

import (
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/tools/integration"
)

type IntegrationTestPrepareInput = integration.IntegrationTestPrepareInput
type ReleaseBuildPrepareInput = integration.ReleaseBuildPrepareInput
type IntegrationApproveInput = integration.IntegrationApproveInput

func registerIntegrationAdminTools(r *toolRegistry, e *integrationqueue.Runtime) {
	newIntegrationService().RegisterAdmin(r.install, e)
}
