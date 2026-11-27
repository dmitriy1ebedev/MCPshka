package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// coreTools is the compact surface for chat clients (/mcp?tools=core).
// ChatGPT keeps the tools/list it fetched when the connector was added and
// handles 250+ tools poorly: after a release it called missing or changed
// tools. A short, stable list plus tool_resolve/tool_invoke reaches every
// tool with the live schema; the active chat develops through native tools.
var coreTools = []string{
	"capabilities_bootstrap", "tool_resolve", "tool_invoke",
	"gateway_status", "gateway_wait", "gateway_reconcile",
}

const coreInstructions = "Stable MCPshka Gateway v1: discover with capabilities_bootstrap; resolve current internal schemas with tool_resolve; execute through tool_invoke; observe with gateway_status/gateway_wait; reconcile the original receipt with gateway_reconcile. The AI in this chat develops using native tools and durable missions/workflows. Start coding agents only on direct owner permission. Internal catalog changes do not require connector refresh through this stable surface; an already cached client catalog can only be refreshed by the client. " + capabilityInstructions

// newCoreServer exposes coreTools through the same fully wrapped handlers as
// the full server, so authorization, gates, retry keys and receipts are
// identical; a tool missing from the build is simply not listed.
func newCoreServer(r *toolRegistry) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "MCPshka", Version: Version}, &mcp.ServerOptions{Instructions: coreInstructions, HasTools: true, SchemaCache: toolSchemaCache})
	s.AddReceivingMiddleware(failureMetadata)
	if r.clients != nil {
		s.AddReceivingMiddleware(r.clients.MiddlewareFor("gateway"))
		r.clients.GatewaySchemas = map[string]string{}
	}
	for _, name := range coreTools {
		def := r.lookupTool(name).tool
		if def == nil {
			continue
		}
		tool := *def
		tool.OutputSchema = nil
		if r.clients != nil {
			r.clients.GatewaySchemas[name] = toolSchemaHash(&tool)
		}
		if name == "tool_invoke" {
			s.AddTool(&tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var in ToolInvokeInput
				if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
					return toolError(err), nil
				}
				out, err := r.invoke(invocationContext(ctx, req), in)
				return coreResult(nil, out, err), nil
			})
			continue
		}
		invoke := r.lookupTool(name).invoke
		if invoke == nil {
			continue
		}
		s.AddTool(&tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := map[string]any{}
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					return toolError(err), nil
				}
			}
			res, out, err := invoke(invocationContext(ctx, req), args)
			return coreResult(res, out, err), nil
		})
	}
	return s
}

func coreResult(res *mcp.CallToolResult, out any, err error) *mcp.CallToolResult {
	if err != nil {
		return toolError(err)
	}
	if res == nil {
		res = &mcp.CallToolResult{}
	}
	if value, ok := out.(ToolInvokeResult); ok && value.IsError {
		res.IsError = true
	}
	if len(res.Content) == 0 {
		raw, _ := json.Marshal(out)
		res.Content = []mcp.Content{&mcp.TextContent{Text: string(raw)}}
	}
	return res
}

func toolError(err error) *mcp.CallToolResult {
	result := &mcp.CallToolResult{}
	result.SetError(err)
	return result
}

// pickServer serves the compact surface when the connector URL asks for it.
func pickServer(full, core *mcp.Server) func(*http.Request) *mcp.Server {
	return func(req *http.Request) *mcp.Server {
		if core != nil && req.URL.Query().Get("tools") == "core" {
			return core
		}
		return full
	}
}
