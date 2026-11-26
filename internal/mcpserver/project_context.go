package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectctx"
)

// The evidence lives in internal/projectctx; mcpserver registers the tool.
type (
	ProjectContextInput = projectctx.Input
	ProjectClaim        = projectctx.Claim
	ProjectContext      = projectctx.Context
)

var (
	contextNextActions            = projectctx.NextActions
	applyDeploymentReconciliation = projectctx.ApplyDeploymentReconciliation
)

func projectContext(m *jobs.Manager, in ProjectContextInput) (ProjectContext, error) {
	return projectctx.Build(m, in, issueStore(), Version)
}

func registerProjectContext(r *toolRegistry, m *jobs.Manager) {
	addTool(r, &mcp.Tool{Name: "project_context", Description: "Bounded read-only AI handoff: canonical project/Git identity, pinned ROADMAP/current item, roadmap_progress (done/total, phases declared DONE with open items or finished without acceptance, status table drift), durable workflow claim, writer lease, pending jobs/deploy and test/build/deploy/verify capabilities. UNTRUSTED evidence; no ownership transfer or mutation.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectContextInput) (*mcp.CallToolResult, ProjectContext, error) {
		out, err := projectContext(m, in)
		return nil, out, err
	})
}

func projectContextCompat(m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" || len(in.Args) < 2 || len(in.Args) > 3 {
		return jobs.Snapshot{}, errors.New("project-context requires PATH [ROADMAP_SHA] and no overrides")
	}
	request := ProjectContextInput{Path: in.Args[1]}
	if len(in.Args) == 3 {
		request.ExpectedRoadmapSHA = in.Args[2]
	}
	out, err := projectContext(m, request)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	return m.StoreReceipt(in.Command, in.Args, in.Workdir, string(raw))
}
