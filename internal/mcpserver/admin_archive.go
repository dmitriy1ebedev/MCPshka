package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/rootbroker"
)

type AdminArchiveInput struct {
	SessionID      string `json:"session_id"`
	Scope          string `json:"scope" jsonschema:"Fixed archive catalogue entry: bootstrap-110"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func registerAdminArchiveTools(r *toolRegistry) {
	closed := false
	for _, action := range []struct {
		name, command, description string
		readOnly                   bool
	}{
		{"admin_archive_prepare", "archive-prepare", "Seal one fixed bootstrap-110 archive plan under the active administrative Face ID session. Pins exact paths, bytes and modes; preparation moves nothing. No caller-selected filesystem paths.", false},
		{"admin_archive_execute", "archive-execute", "Dispatch the prepared fixed archive once through a bounded durable root worker. Repeated commands observe the same intent; UNKNOWN is never relaunched. Existing verified archives are reported as observations, not attributed to this API.", false},
		{"admin_archive_status", "archive-status", "Read the owner-bound archive operation, immutable phase evidence, live worker and actual source/destination state. No archive replay or cleanup.", true},
		{"admin_archive_reconcile", "archive-reconcile", "Reconcile the same archive intent after an absent worker by verifying exact archived bytes and modes. Records an observed effect without replaying a move; unarchived or conflicting state remains HOLD/UNKNOWN.", false},
	} {
		addTool(r, &mcp.Tool{Name: action.name, Description: action.description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: action.readOnly, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminArchiveInput) (*mcp.CallToolResult, rootbroker.ArchiveResult, error) {
			owner, err := adminOwner(ctx)
			if err != nil {
				return nil, rootbroker.ArchiveResult{}, err
			}
			if !action.readOnly && in.IdempotencyKey == "" {
				return nil, rootbroker.ArchiveResult{}, errors.New("archive mutation retry key required")
			}
			out, err := runAdminSession[rootbroker.ArchiveResult](ctx, action.command, in.SessionID, owner, in.Scope)
			return nil, out, err
		})
	}
}
