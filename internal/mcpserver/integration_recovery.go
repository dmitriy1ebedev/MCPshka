package mcpserver

import (
	"context"
	"mcpshka/internal/executor"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/integration"
)

type IntegrationRecoveryInput = integration.IntegrationRecoveryInput

func integrationRecoveryCall(ctx context.Context, m *jobs.Manager, in IntegrationRecoveryInput, status bool) (executor.IntegrationClaim, error) {
	return newIntegrationService().RecoveryCall(ctx, m, in, status)
}
func integrationRecoveryWithApproval(ctx context.Context, m *jobs.Manager, in IntegrationRecoveryInput, status bool, a executor.IntegrationApproval, invoke func(context.Context, string, string, string) ([]byte, error)) (executor.IntegrationClaim, error) {
	return newIntegrationService().RecoveryWithApproval(ctx, m, in, status, a, invoke)
}
func registerIntegrationRecoveryTools(r *toolRegistry, m *jobs.Manager) {
	newIntegrationService().RegisterRecovery(r.install, m)
}

var integrationRecoveryCompatInput = integration.RecoveryCompatInput

func integrationRecoveryCompat(ctx context.Context, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	return newIntegrationService().RecoveryCompat(ctx, m, in)
}

var validIntegrationRecoveryState = integration.ValidRecoveryState
