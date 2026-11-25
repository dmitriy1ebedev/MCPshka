package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/toolcontract"
)

// fillHints sets unset annotation hints (MCP-185): ChatGPT blocked a Lane
// project_commit that carried only readOnlyHint=false (tables in toolcontract).
// An explicit openWorldHint wins; the additive table overrides a stale
// destructive mark (project_build/test only start bounded jobs).
func fillHints(tool *mcp.Tool) {
	if tool.Annotations == nil {
		tool.Annotations = &mcp.ToolAnnotations{}
	}
	a := tool.Annotations
	if a.ReadOnlyHint {
		return
	}
	if a.OpenWorldHint == nil {
		v := toolcontract.OpenWorld(tool.Name)
		a.OpenWorldHint = &v
	}
	if toolcontract.Additive(tool.Name) {
		f := false
		a.DestructiveHint = &f
		a.IdempotentHint = true
	}
}
