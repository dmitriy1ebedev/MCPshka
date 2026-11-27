package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/mcphub"
	"mcpshka/internal/tooldef"
)

// pluginHub is the MCP hub of other servers (MCP-187); Run sets its Dir.
var pluginHub = mcphub.New("")

// toolInvoker runs one registered tool through its complete wrapper:
// scope check, admission gate, retry key, receipts and probes. tool_invoke
// adds nothing and skips nothing, so it never widens what a caller may do.
type toolInvoker func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, any, error)

func invocationContext(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	return tooldef.InvocationContext(ctx, req)
}

func invokerFor[In, Out any](wrapped func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error), tool *mcp.Tool) toolInvoker {
	d := tooldef.New(tool, wrapped)
	return toolInvoker(d.Invoker(d.Handler))
}

type ToolInvokeInput struct {
	Name      string         `json:"name" jsonschema:"Exact MCPshka tool name, e.g. integration_publish_main (see capabilities_bootstrap / tool_resolve)."`
	Arguments map[string]any `json:"arguments,omitempty" jsonschema:"Arguments exactly as the tool's own schema from tool_resolve, including its idempotency_key."`
}

type ToolInvokeResult struct {
	IsError bool     `json:"-"`
	Tool    string   `json:"tool"`
	Output  any      `json:"output,omitempty"`
	Content []string `json:"content,omitempty"`
}

// registerToolInvoke is registered outside addTool: a mutation wrapper around
// the dispatcher would hold the admission gate and a receipt while the target
// takes them again.
func registerToolInvoke(r *toolRegistry) {
	closed := false
	tool := &mcp.Tool{
		Name:        "tool_invoke",
		Description: "Call any MCPshka tool by name when this client's tools/list snapshot lacks it (ChatGPT shows only part of a large catalog). Same authorization, Face ID, retry-key and receipt rules as calling the tool directly; take the exact arguments from tool_resolve. A lost reply is retried with the same arguments and idempotency_key.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed},
	}
	captureCapability[ToolInvokeInput, ToolInvokeResult](r, tool)
	mcp.AddTool(r.server, tool, func(ctx context.Context, req *mcp.CallToolRequest, in ToolInvokeInput) (*mcp.CallToolResult, ToolInvokeResult, error) {
		out, err := r.invoke(invocationContext(ctx, req), in)
		return &mcp.CallToolResult{IsError: out.IsError}, out, err
	})
}

func (r *toolRegistry) invoke(ctx context.Context, in ToolInvokeInput) (ToolInvokeResult, error) {
	if in.Name == "tool_invoke" {
		return ToolInvokeResult{}, errors.New("tool_invoke cannot call itself")
	}
	invoke := r.lookupTool(in.Name).invoke
	if invoke == nil && strings.Contains(in.Name, ".") {
		// <server>.<tool> of a connected MCP server (MCP-187).
		invoke = r.lookupTool("plugin_call").invoke
		args := map[string]any{"name": in.Name}
		inner := map[string]any{}
		for k, v := range in.Arguments {
			if k == "admin_session_id" || k == "idempotency_key" {
				args[k] = v
			} else {
				inner[k] = v
			}
		}
		args["arguments"] = inner
		in.Arguments = args
	}
	if invoke == nil {
		return ToolInvokeResult{}, fmt.Errorf("unknown tool %q; find it with tool_resolve", in.Name)
	}
	res, out, err := invoke(withRoute(ctx, "tool_invoke"), in.Arguments)
	result := ToolInvokeResult{Tool: in.Name, Output: out}
	if res != nil {
		result.IsError = res.IsError
		for _, c := range res.Content {
			if text, ok := c.(*mcp.TextContent); ok {
				result.Content = append(result.Content, text.Text)
			}
		}
	}
	return result, err
}
