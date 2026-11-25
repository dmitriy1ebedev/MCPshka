package mcpserver

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/pipeline"
)

func registerPipelineTool(registry *toolRegistry, d *serverDeps) {
	addTool(registry, &mcp.Tool{Name: "project_pipeline", Description: "Run reviewed project profile stages in the sealed executor and heavy queue. One stage per call; new key polls/progresses, same key replays the original response. PASS evidence is reused only for identical clean HEAD, tree and full profile digest. Package exports declared files with SHA-256 and manifest; retained seven days.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in pipeline.Input) (*mcp.CallToolResult, pipeline.Result, error) {
		payload := struct {
			Path   string
			Stages []string
			Holder string
		}{in.Path, in.Stages, holderTokenDigest(in.LeaseToken)}
		out, err := runIdempotent(d.idempotencyStore, in.IdempotencyKey, "project_pipeline", payload, func() (pipeline.Result, error) {
			return pipeline.Advance(ctx, pipeline.Dependencies{Manager: d.manager, Git: d.git, Profiles: d.profiles, Options: profileJobOptions}, in)
		})
		return nil, out, err
	})
}
