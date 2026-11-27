package mcpserver

import (
	"context"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/integration"
)

type IntegrationEnqueueInput = integration.IntegrationEnqueueInput
type IntegrationStatusInput = integration.IntegrationStatusInput
type IntegrationCancelInput = integration.IntegrationCancelInput

func registerIntegrationTools(r *toolRegistry, e *integrationqueue.Runtime) {
	newIntegrationService().RegisterQueue(r.install, e)
}

var isIntegrationMutation = integration.IsMutation
var decodeIntegrationInput = integration.DecodeInput

func integrationCompat(ctx context.Context, e *integrationqueue.Runtime, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	return newIntegrationService().Compat(ctx, e, m, in)
}
