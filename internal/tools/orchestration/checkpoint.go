package orchestration

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/failure"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/phasegate"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/adminops"
)

func registerCheckpointSupersede(add func(tooldef.Def), d RoadmapDeps, git *gitops.Service, manager *jobs.Manager) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "phase_checkpoint_supersede", Description: "Record one owner-approved checkpoint scope revision under Face ID and the project lease. Pins old checkpoint, old and revised ROADMAP digests, owner decision and successor. Keeps the original criterion as history. phase_gate skips the revised criterion; all other gates still apply.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed, IdempotentHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in phasegate.SupersedeInput) (*mcp.CallToolResult, phasegate.SupersedeResult, error) {
		owner, err := adminops.Owner(ctx)
		if err != nil {
			return nil, phasegate.SupersedeResult{}, failure.Rejected("OWNER_REQUIRED", "phase_checkpoint_supersede", "authenticated owner required", err)
		}
		session, err := adminops.Run[rootbroker.AdminSessionView](ctx, "status", in.SessionID, owner)
		if err != nil || in.SessionID == "" || session.ID != in.SessionID || session.Owner != owner || session.AuthMethod != "webauthn" || session.State != "active" {
			return nil, phasegate.SupersedeResult{}, failure.Rejected("FACE_ID_REQUIRED", "phase_checkpoint_supersede", "active owner Face ID session required", err)
		}
		var result phasegate.SupersedeResult
		err = d.Leases.WithInteractiveHolder(in.Path, in.LeaseToken, func() error {
			var err error
			result, err = phasegate.Supersede(ctx, git, manager.Database(), manager.StateDir(), in, owner, time.Now().UTC())
			return err
		})
		return nil, result, err
	}))
}
