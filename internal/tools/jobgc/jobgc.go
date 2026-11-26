// Package jobgc exposes the guarded job artifact GC as MCP tools (MCP-274).
package jobgc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
)

// Preview cannot mutate; apply is a separate, Face ID-gated MCP operation.
type ApplyInput struct {
	PlanSHA256     string `json:"plan_sha256" jsonschema:"SHA-256 of the exact current preview"`
	AdminSessionID string `json:"admin_session_id" jsonschema:"active owner Face ID session"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"stable retry key"`
}

// Register installs preview and apply; requireAdmin admits apply only under the active owner Face ID session.
func Register(add func(tooldef.Def), manager *jobs.Manager, store *idempotency.Store, requireAdmin func(context.Context, string) error) {
	destructive := true
	add(tooldef.New(&mcp.Tool{
		Name:        "job_artifact_gc_preview",
		Description: "Read-only, bounded SHA-pinned inventory of reproducible compiler scratch under terminal MCPshka jobs. Keeps SQLite receipts, logs, unknown/leased/referenced workflows; 72 hours successful, 96 hours unsuccessful.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in toolcontract.EmptyInput) (*mcp.CallToolResult, jobs.ArtifactGCReport, error) {
		result, err := manager.ArtifactGC(time.Now().UTC(), false, "")
		return nil, result, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "job_artifact_gc_apply",
		Description: "Apply exactly the previously previewed SHA-256 plan after active owner Face ID and idempotent receipt. Deletes only old disposable Go build scratch. No metadata, stdout/stderr, UNKNOWN, active, lease-held, or workflow evidence is deleted.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ApplyInput) (*mcp.CallToolResult, jobs.ArtifactGCReport, error) {
		if len(in.PlanSHA256) != 64 || strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, jobs.ArtifactGCReport{}, errors.New("apply requires current preview plan_sha256 and idempotency_key")
		}
		if err := requireAdmin(ctx, in.AdminSessionID); err != nil {
			return nil, jobs.ArtifactGCReport{}, err
		}
		result, err := idempotency.Run(store, in.IdempotencyKey, "job_artifact_gc_apply", in, func() (jobs.ArtifactGCReport, error) {
			return manager.ArtifactGC(time.Now().UTC(), true, in.PlanSHA256)
		})
		return nil, result, err
	}))
}
