package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGatewayPreservesSessionAndMCPError(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	r := &toolRegistry{server: server, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	addTool(r, &mcp.Tool{Name: "fixture_session", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, req *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"session_present": req != nil && req.Session != nil}, nil
	})
	addTool(r, &mcp.Tool{Name: "fixture_error", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest, EmptyInput) (*mcp.CallToolResult, map[string]any, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "fixture executor refusal"}}}, nil, nil
	})
	registerToolInvoke(r)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "stale-client", Version: "1"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	t.Run("session", func(t *testing.T) {
		text, bad := callText(t, cs, "tool_invoke", map[string]any{"name": "fixture_session"})
		var out struct {
			Output struct {
				Present bool `json:"session_present"`
			}
		}
		if bad || json.Unmarshal([]byte(text), &out) != nil || !out.Output.Present {
			t.Fatal("gateway lost MCP session", text)
		}
	})
	t.Run("executor_error", func(t *testing.T) {
		text, bad := callText(t, cs, "tool_invoke", map[string]any{"name": "fixture_error"})
		if !bad {
			t.Fatal("gateway converted MCP executor refusal to success", text)
		}
	})
}
