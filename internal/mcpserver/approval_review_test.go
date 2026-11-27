package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/tools/approvedtask"
)

func TestMCP246ApprovalReviewResourceThroughMCP(t *testing.T) {
	c := fullCatalogClient(t)
	for _, tool := range catalogTools(t, c) {
		if tool.Name != "approved_task_prepare" {
			continue
		}
		ui, _ := tool.Meta["ui"].(map[string]any)
		if ui["resourceUri"] != approvedtask.ReviewURI {
			t.Fatal("prepare tool does not reference review resource")
		}
		res, err := c.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: approvedtask.ReviewURI})
		if err != nil || len(res.Contents) != 1 {
			t.Fatalf("approval review unavailable: %v", err)
		}
		content := res.Contents[0]
		if content.MIMEType != "text/html;profile=mcp-app" ||
			!strings.Contains(content.Text, "ui/open-link") ||
			strings.Count(content.Text, "tools/call") != 1 ||
			!strings.Contains(content.Text, `name:"get_tool_result"`) ||
			strings.Contains(content.Text, "approved_task_execute") {
			t.Fatal("review must open the guarded portal and read only its durable status")
		}
		return
	}
	t.Fatal("prepare tool is absent")
}
