package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
)

func TestLeaseReplayCannotResurrectReleasedContractReceipt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	m, err := jobs.New(filepath.Join(t.TempDir(), "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	server := New(m, filesystem.New(p))
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	args := map[string]any{"path": root, "owner": "same-owner", "idempotency_key": "first"}
	first, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_lease_acquire", Arguments: args})
	if err != nil || first.IsError {
		t.Fatal("first acquire failed")
	}
	raw, _ := json.Marshal(first.StructuredContent)
	var held projectlease.AcquireResult
	if err = json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	released, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_lease_release", Arguments: map[string]any{"path": root, "token": held.Token, "idempotency_key": "release-first"}})
	if err != nil || released.IsError {
		t.Fatal("release failed")
	}
	replay, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_lease_acquire", Arguments: args})
	if err == nil && !replay.IsError {
		t.Fatal("outer contract cache resurrected released lease")
	}
	args["idempotency_key"] = "second"
	second, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_lease_acquire", Arguments: args})
	if err != nil || second.IsError {
		t.Fatal("new acquire failed")
	}
	args["idempotency_key"] = "first"
	replay, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_lease_acquire", Arguments: args})
	if err == nil && !replay.IsError {
		t.Fatal("same-owner replacement accepted stale token")
	}
}
