package mcpserver

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/tooldef"
)

// schemaHints adds the next correct call to the SDK's bare schema refusal
// of a direct tools/call (tool_invoke adds it in tooldef): accepted and
// required fields, a near match for a misspelt one and a minimal example.
func schemaHints(r *toolRegistry) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, request)
			if method != "tools/call" || err != nil {
				return result, err
			}
			out, ok := result.(*mcp.CallToolResult)
			if !ok || out == nil || !out.IsError || len(out.Content) == 0 {
				return result, err
			}
			text, ok := out.Content[0].(*mcp.TextContent)
			if !ok || !strings.Contains(text.Text, `validating "arguments"`) {
				return result, err
			}
			params, ok := request.GetParams().(*mcp.CallToolParamsRaw)
			if !ok || params == nil {
				return result, err
			}
			tool := r.lookupTool(params.Name).tool
			if tool == nil || tool.InputSchema == nil {
				return result, err
			}
			var schema jsonschema.Schema
			raw, _ := json.Marshal(tool.InputSchema)
			if json.Unmarshal(raw, &schema) != nil {
				return result, err
			}
			args := map[string]any{}
			_ = json.Unmarshal(params.Arguments, &args)
			copy := *out
			copy.Content = append([]mcp.Content{&mcp.TextContent{Text: text.Text + "; " + tooldef.SchemaHint(&schema, args)}}, out.Content[1:]...)
			return &copy, nil
		}
	}
}
