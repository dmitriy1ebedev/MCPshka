package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A direct call with a misspelt field gets the correct call back.
func TestDirectCallSchemaRefusalNamesTheFix(t *testing.T) {
	c := fullCatalogClient(t)
	res, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "memory_recall", Arguments: map[string]any{"text": "x", "limt": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || len(res.Content) == 0 {
		t.Fatalf("accepted a misspelt field: %+v", res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "«limt» → «limit»") || !strings.Contains(text, "accepted:") {
		t.Fatalf("no hint: %s", text)
	}
}
