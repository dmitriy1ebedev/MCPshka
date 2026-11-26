package mcpserver

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/gitcontrol"
	"mcpshka/internal/jobs"
	"mcpshka/internal/rootbroker"
)

func gitProviderAdmin(ctx context.Context, session string) error {
	if session == "" {
		return errors.New("owner-bound Face ID session required")
	}
	owner, err := adminOwner(ctx)
	if err != nil {
		return err
	}
	view, err := runAdminSession[rootbroker.AdminSessionView](ctx, "status", session, owner)
	if err != nil {
		return err
	}
	if view.ID != session || view.State != "active" {
		return errors.New("Face ID session inactive or not owned by caller")
	}
	return nil
}
func registerGitProviderTools(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "git_provider_query", Description: "Read typed GitHub/GitVerse/MosHub repository metadata, authenticated identity, branches, supported actions and CI. No arbitrary URL or token.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitcontrol.QueryInput) (*mcp.CallToolResult, map[string]any, error) {
		v, e := gitcontrol.Query(ctx, in)
		return nil, v, e
	})
	addTool(r, &mcp.Tool{Name: "git_provider_change", Description: "Owner-approved typed GitHub/GitVerse/MosHub repository creation/update/collaborator and genuine public GitHub fork with parent read-back, protected credentials and idempotency.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitcontrol.ChangeInput) (*mcp.CallToolResult, gitcontrol.ChangeResult, error) {
		v, e := gitcontrol.Change(ctx, in, r.store, gitProviderAdmin, adminOwner)
		return nil, v, e
	})
}
func registerGitProviderPush(r *toolRegistry, m *jobs.Manager) {
	addTool(r, &mcp.Tool{Name: "git_provider_push", Description: "FaceID+leased bounded Git HEAD push: exact registered HTTPS remote, history scan, no force and remote SHA verification.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitcontrol.PushInput) (*mcp.CallToolResult, jobs.Snapshot, error) {
		v, e := gitcontrol.Push(ctx, in, m, r.store, gitProviderAdmin, adminOwner)
		return nil, v, e
	})
}
