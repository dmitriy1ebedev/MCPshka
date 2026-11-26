package integration

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/executor"
)

type Service struct {
	Owner      func(context.Context) string
	AdminOwner func(context.Context) (string, error)
	Admin      func(context.Context, ...string) (map[string]any, error)
	Authorize  func(context.Context, *mcp.Tool) error
	Published  func(context.Context, executor.IntegrationApproval)
}
