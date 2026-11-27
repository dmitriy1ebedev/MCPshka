package mcpserver

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/repoadopt"
)

type ProjectRepoInput struct {
	SessionID string `json:"admin_session_id" jsonschema:"Active Face ID window."`
	Path      string `json:"path" jsonschema:"Target /root/repos/<name>: created if absent, handed to the service user if root left it."`
	Source    string `json:"source,omitempty" jsonschema:"Optional /root/<name> project directory to move into path (path must be absent)."`
}

type ProjectRepoAdoptInput struct {
	ProjectRepoInput
	PlanSHA256     string `json:"plan_sha256" jsonschema:"plan_sha256 from project_repo_preview; any change to the tree since then holds the apply."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

func projectRepoSource(source string) string {
	if source == "" {
		return "-"
	}
	return filepath.Clean(source)
}

func registerProjectRepoTools(r *toolRegistry) {
	closed, destructive := false, false
	addTool(r, &mcp.Tool{
		Name:        "project_repo_preview",
		Description: "Plan making a project MCPshka's own under the Face ID window: create /root/repos/<name>, move /root/<name> there, or hand a root-owned project (root git checkout, files left by root) to the service user. Reports entries, how many change owner, rejected entries (hard links while fs.protected_hardlinks is off, devices, FIFOs, other filesystems) and plan_sha256. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectRepoInput) (*mcp.CallToolResult, repoadopt.Plan, error) {
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, repoadopt.Plan{}, err
		}
		plan, err := runAdminSession[repoadopt.Plan](ctx, "project-adopt", in.SessionID, owner, "preview", filepath.Clean(in.Path), projectRepoSource(in.Source))
		return nil, plan, err
	})
	addTool(r, &mcp.Tool{
		Name:        "project_repo_adopt",
		Description: "Apply exactly the project_repo_preview plan as root: re-walks the tree and refuses any drift, then creates/moves the directory and chowns it to the service user through descriptors opened without following symlinks. Face ID window required; protected root repositories are refused.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectRepoAdoptInput) (*mcp.CallToolResult, repoadopt.Plan, error) {
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, repoadopt.Plan{}, err
		}
		if len(in.PlanSHA256) != 64 {
			return nil, repoadopt.Plan{}, errors.New("plan_sha256 from project_repo_preview is required")
		}
		out, err := runIdempotent(r.store, in.IdempotencyKey, "project_repo_adopt", in, func() (repoadopt.Plan, error) {
			return runAdminSession[repoadopt.Plan](ctx, "project-adopt", in.SessionID, owner, "apply", filepath.Clean(in.Path), projectRepoSource(in.Source), in.PlanSHA256)
		})
		return nil, out, err
	})
}
