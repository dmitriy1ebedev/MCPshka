package mcpserver

import (
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/integration"
)

type IntegrationPublishInput = integration.IntegrationPublishInput
type IntegrationPublishResult = integration.IntegrationPublishResult

func registerIntegrationPublishTool(r *toolRegistry, m *jobs.Manager, guard func(string, string) error, queue *integrationqueue.Runtime) {
	newIntegrationService().RegisterPublish(r.install, m, guard, queue)
}
