package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/workflow"
)

type PipelineStartInput struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Pipeline       string `json:"pipeline"`
	RoadmapSHA     string `json:"roadmap_sha256"`
	Scope          string `json:"scope"`
	IdempotencyKey string `json:"idempotency_key"`
}
type PipelinePlanInput struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Pipeline   string `json:"pipeline"`
	RoadmapSHA string `json:"roadmap_sha256"`
	Scope      string `json:"scope"`
}
type PipelineInput struct {
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func registerPipelineTools(r *toolRegistry, e *workflow.Engine) {
	addTool(r, &mcp.Tool{Name: "workflow_plan", Description: "Read-only exact preview of an existing trusted ten-stage project profile pipeline: original ID, root, clean starting HEAD, ROADMAP digest, scope and profile fingerprint. Creates no mission, claim or job. Pass this unchanged as mission_start.workflow_plan to authorize native pipeline execution; workflow_start scope is also required.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in PipelinePlanInput) (*mcp.CallToolResult, projectprofile.PipelinePin, error) {
		out, err := e.Preview(in.Path, in.Pipeline, in.ID, in.RoadmapSHA, in.Scope)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "workflow_status", Description: "Read the durable profile pipeline, its pinned worktree, roadmap, scope, starting revision and current job. Missing receipts remain HOLD.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in PipelineInput) (*mcp.CallToolResult, workflow.Run, error) {
		out, err := e.Get(in.ID)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "workflow_start", Description: "Claim an idle unleased Git worktree for a trusted ten-stage profile pipeline. Requires exact ROADMAP digest and phase scope. Background maintenance advances bounded jobs; transport retries never replay stages.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in PipelineStartInput) (*mcp.CallToolResult, workflow.Run, error) {
		out, err := e.Start(in.Path, in.Pipeline, in.ID, in.RoadmapSHA, in.Scope)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "workflow_resume", Description: "Reconcile HOLD using pinned plans and actual job receipts. UNKNOWN or missing exits cannot authorize replay. Recovery uses the profile rollback and finalizer.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in PipelineInput) (*mcp.CallToolResult, workflow.Run, error) {
		out, err := e.Resume(in.ID)
		return nil, out, err
	})
}

// Compatibility keeps already-connected clients usable before a schema refresh.
func pipelineCompat(e *workflow.Engine, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return jobs.Snapshot{}, errors.New("pipeline overrides forbidden")
	}
	var out workflow.Run
	var err error
	switch {
	case len(in.Args) == 7 && in.Args[1] == "start":
		out, err = e.Start(in.Args[3], in.Args[4], in.Args[2], in.Args[5], in.Args[6])
	case len(in.Args) == 3 && in.Args[1] == "status":
		out, err = e.Get(in.Args[2])
	case len(in.Args) == 3 && in.Args[1] == "resume":
		out, err = e.Resume(in.Args[2])
	default:
		return jobs.Snapshot{}, errors.New("pipeline requires start ID PATH PIPELINE ROADMAP_SHA SCOPE, status ID, or resume ID")
	}
	if err != nil {
		return jobs.Snapshot{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	return m.StoreReceipt(in.Command, in.Args, in.Workdir, string(raw))
}
