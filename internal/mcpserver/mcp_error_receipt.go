package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
)

// Error receipts keep a bounded diagnostic; arbitrary result content stays private.
func mcpErrorText(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range result.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok || text == nil || text.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text.Text[:min(len(text.Text), 2048-b.Len())])
		if b.Len() >= 2048 {
			break
		}
	}
	if b.Len() == 0 {
		return "Tool reported an MCP error."
	}
	return strings.ToValidUTF8(b.String(), "")
}

func receiptErrorContent(text string) []mcp.Content {
	if text == "" {
		return nil
	}
	return []mcp.Content{&mcp.TextContent{Text: text}}
}
