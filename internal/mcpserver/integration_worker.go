package mcpserver

import (
	"context"
	"mcpshka/internal/executor"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/integration"
)

type IntegrationWorkerInput = integration.IntegrationWorkerInput
type IntegrationWorkerOutput = integration.IntegrationWorkerOutput

var integrationWorkerAction = integration.WorkerAction
var integrationWorkerInputValid = integration.WorkerInputValid

func integrationWorkerCall(ctx context.Context, m *jobs.Manager, in IntegrationWorkerInput) (IntegrationWorkerOutput, error) {
	return newIntegrationService().WorkerCall(ctx, m, in)
}
func integrationWorkerWithApproval(ctx context.Context, m *jobs.Manager, in IntegrationWorkerInput, a executor.IntegrationApproval, invoke func(context.Context, string, string, string) ([]byte, error)) (IntegrationWorkerOutput, error) {
	return newIntegrationService().WorkerWithApproval(ctx, m, in, a, invoke)
}

var integrationWorkerClaimValid = integration.WorkerClaimValid
var decodeIntegrationWorkerOutput = integration.DecodeWorkerOutput

func registerIntegrationWorkerTools(r *toolRegistry, m *jobs.Manager) {
	newIntegrationService().RegisterWorker(r.install, m)
}

var integrationWorkerCompatInput = integration.WorkerCompatInput

func integrationWorkerCompat(ctx context.Context, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	return newIntegrationService().WorkerCompat(ctx, m, in)
}
