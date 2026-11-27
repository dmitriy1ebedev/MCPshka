package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/tooldef"
)

var mcpErrorText = tooldef.MCPErrorText

func receiptErrorContent(text string) []mcp.Content {
	if text == "" {
		return nil
	}
	return []mcp.Content{&mcp.TextContent{Text: text}}
}
