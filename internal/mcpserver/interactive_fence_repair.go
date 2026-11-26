package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/rootbroker"
)

type InteractiveFenceRepairInput struct {
	Path           string                                  `json:"path"`
	SessionID      string                                  `json:"session_id"`
	Plan           projectlease.InteractiveFenceRepairPlan `json:"plan"`
	IdempotencyKey string                                  `json:"idempotency_key"`
}

func registerInteractiveFenceRepairTools(r *toolRegistry, manager *jobs.Manager) {
	closed := false
	addTool(r, &mcp.Tool{Name: "project_lease_admin_fence_preview", Description: "Inspect an immediate /root/repos repository's interactive fence under the authenticated owner's Face ID grant. Return exact repair pins or service-owned state; no ownership change or lease transfer.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminLegacyLeasePreviewInput) (*mcp.CallToolResult, rootbroker.InteractiveFencePreview, error) {
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, rootbroker.InteractiveFencePreview{}, err
		}
		out, err := runAdminSession[rootbroker.InteractiveFencePreview](ctx, "preview-interactive-fence", in.SessionID, owner, in.Path)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "project_lease_admin_fence_repair", Description: "Repair only a pinned root-owned empty interactive fence for an idle immediate /root/repos repository, preserving its inode and flock. Active owner-bound Face ID grant, lease-absent writer gate, durable journal and stopped-writer proof are required. UNKNOWN requires exact reconciliation; never removes a lock or takes over a lease.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in InteractiveFenceRepairInput) (*mcp.CallToolResult, projectlease.InteractiveFenceRepairResult, error) {
		var out projectlease.InteractiveFenceRepairResult
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, out, err
		}
		if in.IdempotencyKey == "" || in.Path != in.Plan.Path || in.Plan.Authorization != "" {
			return nil, out, errors.New("exact pinned repair scope and retry key required")
		}
		raw, err := json.Marshal(in.Plan)
		if err != nil {
			return nil, out, err
		}
		err = manager.WithIdleProject(in.Path, func() error {
			var e error
			out, e = runAdminSession[projectlease.InteractiveFenceRepairResult](ctx, "repair-interactive-fence", in.SessionID, owner, string(raw))
			return e
		})
		return nil, out, err
	})
}

type StaleLeaseReleaseInput struct {
	Path           string                        `json:"path"`
	SessionID      string                        `json:"session_id"`
	Plan           projectlease.StaleReleasePlan `json:"plan"`
	IdempotencyKey string                        `json:"idempotency_key"`
}

func registerStaleLeaseTools(r *toolRegistry, manager *jobs.Manager) {
	closed := false
	addTool(r, &mcp.Tool{Name: "project_lease_admin_stale_preview", Description: "Prove a lease's holder is gone (bound owner process dead, or a remote chat silent 30 min or its agent_task ended; no project job, deployment or process using the worktree) under the owner's Face ID window and return the pinned release plan. Read-only; nothing is released.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminLegacyLeasePreviewInput) (*mcp.CallToolResult, projectlease.StaleReleasePlan, error) {
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, projectlease.StaleReleasePlan{}, err
		}
		out, err := runAdminSession[projectlease.StaleReleasePlan](ctx, "preview-stale-lease", in.SessionID, owner, in.Path)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "project_lease_admin_stale_release", Description: "Release exactly the pinned stale lease from project_lease_admin_stale_preview after re-proving the holder is gone and the project has no jobs. Face ID window required; never transfers the lease to anyone.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in StaleLeaseReleaseInput) (*mcp.CallToolResult, projectlease.ReleaseResult, error) {
		var out projectlease.ReleaseResult
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, out, err
		}
		if in.IdempotencyKey == "" || in.Path != in.Plan.Path {
			return nil, out, errors.New("exact pinned release scope and retry key required")
		}
		raw, err := json.Marshal(in.Plan)
		if err != nil {
			return nil, out, err
		}
		err = manager.WithIdleProject(in.Path, func() error {
			var e error
			out, e = runAdminSession[projectlease.ReleaseResult](ctx, "release-stale-lease", in.SessionID, owner, string(raw))
			return e
		})
		return nil, out, err
	})
}
