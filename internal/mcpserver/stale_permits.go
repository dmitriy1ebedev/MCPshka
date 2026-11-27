package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/executor"
)

type StalePermitsPreviewInput struct {
	SessionID string `json:"session_id"`
}
type StalePermitsReconcileInput struct {
	SessionID      string                   `json:"session_id"`
	Plan           executor.StalePermitPlan `json:"plan"`
	IdempotencyKey string                   `json:"idempotency_key"`
}

func registerStalePermitTools(r *toolRegistry) {
	closed := false
	addTool(r, &mcp.Tool{Name: "executor_admin_stale_preview", Description: "Read-only root reconciliation preview under the owner's active Face ID admin_session. List permits older than one hour with no live executor unit or processes, pinned by record SHA-256. Legacy age requires trusted evidence; unprovable records are skipped. No admission change or stop.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in StalePermitsPreviewInput) (*mcp.CallToolResult, executor.StalePermitPreview, error) {
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, executor.StalePermitPreview{}, err
		}
		out, err := runAdminSession[executor.StalePermitPreview](ctx, "preview-stale-permits", in.SessionID, owner, "all")
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "executor_admin_stale_reconcile", Description: "Under an owner-bound Face ID admin_session, recheck the exact pinned preview plan and transition only proven abandoned permits to terminal FENCED with atomic audit receipts. Holds launch admission locks; never kills processes, stops units, releases leases or invents successful execution. Retry with the same key; preview current authority after UNKNOWN.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in StalePermitsReconcileInput) (*mcp.CallToolResult, executor.StalePermitResult, error) {
		var out executor.StalePermitResult
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, out, err
		}
		if in.IdempotencyKey == "" || !executor.ValidStalePermitPlan(in.Plan) {
			return nil, out, errors.New("pinned stale permit plan and retry key required")
		}
		raw, err := json.Marshal(in.Plan)
		if err != nil {
			return nil, out, err
		}
		out, err = runAdminSession[executor.StalePermitResult](ctx, "reconcile-stale-permits", in.SessionID, owner, string(raw))
		return nil, out, err
	})
}
