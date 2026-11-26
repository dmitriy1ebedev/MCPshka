package mcpserver

import (
	"context"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/statestore"
	codextools "mcpshka/internal/tools/codex"
)

type CodexSessionsInput = codextools.CodexSessionsInput
type CodexSessionInput = codextools.CodexSessionInput
type CodexTaskInput = codextools.CodexTaskInput
type CodexTaskProbeInput = codextools.CodexTaskProbeInput
type CodexTaskStatusInput = codextools.CodexTaskStatusInput
type CodexCapacityStatus = codextools.CodexCapacityStatus
type CodexAdmissionTaskInput = codextools.CodexAdmissionTaskInput
type CodexAdmissionModelInput = codextools.CodexAdmissionModelInput
type CodexAdmissionPreviewInput = codextools.CodexAdmissionPreviewInput
type CodexAdmissionPreviewOutput = codextools.CodexAdmissionPreviewOutput
type CodexIntentCreateInput = codextools.CodexIntentCreateInput
type CodexIntentUpdateInput = codextools.CodexIntentUpdateInput
type CodexIntentCancelInput = codextools.CodexIntentCancelInput
type CodexIntentIDInput = codextools.CodexIntentIDInput
type CodexIntentListInput = codextools.CodexIntentListInput
type CodexQueueIDInput = codextools.CodexQueueIDInput
type CodexQueuePageInput = codextools.CodexQueuePageInput
type CodexQueuePlanInput = codextools.CodexQueuePlanInput
type CodexQueueSummary = codextools.CodexQueueSummary
type CodexQueueReadResult = codextools.CodexQueueReadResult
type CodexQueuePageResult = codextools.CodexQueuePageResult
type CodexRoutePreviewInput = codextools.CodexRoutePreviewInput
type CodexRoutePreviewOutput = codextools.CodexRoutePreviewOutput

type codexUnknownError struct{ error }

func (codexUnknownError) UnknownOutcome() bool { return true }

var codexRPC = codextools.BrokerRPC

func registerCodexTools(r *toolRegistry, store *idempotency.Store) {
	codextools.Register(r.install, codextools.Deps{Store: store, RPC: func(ctx context.Context, op string, in any) (map[string]any, error) {
		return codexRPC(ctx, op, in)
	}})
}
func registerCodexAdmissionTools(r *toolRegistry) { codextools.RegisterAdmission(r.install) }
func registerCodexIntentTools(r *toolRegistry, db *statestore.DB) {
	codextools.RegisterIntents(r.install, db)
}
func registerCodexQueueTools(r *toolRegistry, db *statestore.DB) {
	codextools.RegisterQueue(r.install, db)
}
func registerCodexRoutePreview(r *toolRegistry) { codextools.RegisterRoute(r.install) }
