package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestHiddenToolsLeaveListButStayCallable: MCP-163 shortens tools/list, yet a
// hidden tool keeps its handler, is offered by tool_resolve and answers by name.
func TestHiddenToolsLeaveListButStayCallable(t *testing.T) {
	c := catalogClient(t, nil)
	listed := map[string]bool{}
	for _, tool := range catalogTools(t, c) {
		listed[tool.Name] = true
		if hiddenTool(tool.Name) {
			t.Errorf("%s is hidden but still in tools/list", tool.Name)
		}
	}
	if !listed["capabilities_bootstrap"] || !listed["tool_resolve"] || !listed["tool_invoke"] || !listed["code_run"] || !listed["mcpshka_guide"] {
		t.Fatal("the filter dropped live tools")
	}
	ctx := context.Background()
	res, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "tool_resolve", Arguments: map[string]any{"goal": "codex_capacity_status"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"name":"codex_capacity_status"`) {
		t.Fatalf("tool_resolve must still offer a hidden tool: %s", raw)
	}
	if _, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "codex_capacity_status", Arguments: map[string]any{}}); err != nil && strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("a hidden tool must answer by name: %v", err)
	}
}

// The autorelease watcher and tool_invoke reach integration_ship by name: in
// 0.11.2 it had no invoker and release_apply action=queue stopped at SHIP.
func TestGitProviderReadResolveOnly(t *testing.T) {
	if !hiddenTool("git_provider_query") {
		t.Fatal("provider query must be resolve-only to retain the 250-tool budget")
	}
	c := catalogClient(t, nil)
	for _, tool := range catalogTools(t, c) {
		if tool.Name == "git_provider_query" {
			t.Fatal("query leaked to tools/list")
		}
	}
	result, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "tool_resolve", Arguments: map[string]any{"goal": "git_provider_query"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(raw), `"name":"git_provider_query"`) {
		t.Fatalf("hidden provider query not discoverable: %s", raw)
	}
}

func TestIntegrationShipCallableByName(t *testing.T) {
	c := catalogClient(t, nil)
	res, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "tool_invoke", Arguments: map[string]any{"name": "integration_ship", "arguments": map[string]any{}}})
	text := ""
	if err != nil {
		text = err.Error()
	} else {
		raw, _ := json.Marshal(res)
		text = string(raw)
	}
	if strings.Contains(text, "unknown tool") {
		t.Fatalf("integration_ship is not callable by name: %s", text)
	}
}
