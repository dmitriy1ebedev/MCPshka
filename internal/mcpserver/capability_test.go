package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/authn"
)

func TestAuthorizeToolRoleAndScope(t *testing.T) {
	readTool := &mcp.Tool{
		Name:        "device_status",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}
	writeTool := &mcp.Tool{
		Name:        "fs_patch",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false},
	}

	reader := authn.WithIdentity(context.Background(), authn.Identity{
		ID: "reader-ai", Source: "registry", Role: "reader", Scopes: []string{"device_status"},
	})
	if err := authorizeTool(reader, readTool); err != nil {
		t.Fatalf("reader denied allowed read tool: %v", err)
	}
	if err := authorizeTool(reader, writeTool); err == nil {
		t.Fatal("reader unexpectedly allowed mutating tool")
	}

	operatorWrongScope := authn.WithIdentity(context.Background(), authn.Identity{
		ID: "operator-ai", Source: "registry", Role: "operator", Scopes: []string{"device_status"},
	})
	if err := authorizeTool(operatorWrongScope, writeTool); err == nil {
		t.Fatal("operator without tool scope unexpectedly allowed")
	}

	operator := authn.WithIdentity(context.Background(), authn.Identity{
		ID: "operator-ai", Source: "registry", Role: "operator", Scopes: []string{"fs_patch"},
	})
	if err := authorizeTool(operator, writeTool); err != nil {
		t.Fatalf("operator with exact scope denied: %v", err)
	}

	wildcard := authn.WithIdentity(context.Background(), authn.Identity{
		ID: "operator-all", Source: "registry", Role: "operator", Scopes: []string{"*"},
	})
	if err := authorizeTool(wildcard, writeTool); err != nil {
		t.Fatalf("operator wildcard denied: %v", err)
	}
}

func TestAuthorizeToolAllowsTrustedInProcessCaller(t *testing.T) {
	tool := &mcp.Tool{Name: "fs_patch", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}
	if err := authorizeTool(context.Background(), tool); err != nil {
		t.Fatalf("trusted in-process caller denied: %v", err)
	}
}
