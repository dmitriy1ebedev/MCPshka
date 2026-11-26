package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestApprovalRequestRequiresTrustedPlanHash(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	p := policy.Default()
	p.AllowedWorkdirs = []string{root}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	server := New(manager, filesystem.New(p))
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	missing, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "approval_request",
		Arguments: map[string]any{
			"idempotency_key": "missing-plan",
			"command":         "rm",
			"args":            []string{"-f", "target"},
			"workdir":         root,
		},
	})
	if err == nil && !missing.IsError {
		t.Fatalf("approval_request without plan_hash unexpectedly succeeded: %+v", missing)
	}

	valid, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "approval_request",
		Arguments: map[string]any{
			"idempotency_key": "valid-plan",
			"command":         "rm",
			"args":            []string{"-f", "target"},
			"workdir":         root,
			"plan_hash":       strings.Repeat("a", 64),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if valid.IsError {
		data, _ := json.Marshal(valid.Content)
		t.Fatalf("approval_request with plan_hash failed: %s", data)
	}
}
