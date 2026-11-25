package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/gitops"
	"mcpshka/internal/salvage"
)

type SalvagePreviewInput struct {
	Path           string   `json:"path" jsonschema:"repository under an allowed root"`
	Base           string   `json:"base,omitempty" jsonschema:"base branch, default main"`
	Branches       []string `json:"branches,omitempty" jsonschema:"Local branches to inspect; default all local branches, at most 64 per bounded observation."`
	IncludeCommits bool     `json:"include_commits,omitempty" jsonschema:"Include bounded patch details; default false keeps the inventory compact. Select at most four branches for details."`
}

// MCP-054: read-only inventory of agent branches before any handoff or pick.
func registerSalvageTool(r *toolRegistry, git *gitops.Service) {
	closed := false
	addTool(r, &mcp.Tool{
		Name:        "agent_salvage_preview",
		Description: "Bounded read-only local branch inventory against a pinned base: ancestry, patch identity and exact changed-path content. Reports integrated, superseded, needs_review, wip or unknown; preserves untracked work and observation failures. Git equivalence never proves release acceptance or permission to delete. Compact by default; select at most four branches for commit details.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in SalvagePreviewInput) (*mcp.CallToolResult, salvage.Report, error) {
		if in.IncludeCommits && (len(in.Branches) == 0 || len(in.Branches) > 4) {
			return nil, salvage.Report{}, errors.New("select one to four branches for detailed inventory")
		}
		root, err := git.RepoRoot(in.Path)
		if err != nil {
			return nil, salvage.Report{}, err
		}
		out, err := salvage.Preview(ctx, root, in.Base, in.Branches)
		if !in.IncludeCommits {
			for i := range out.Candidates {
				out.Candidates[i].Commits = nil
			}
		}
		return nil, out, err
	})
}
