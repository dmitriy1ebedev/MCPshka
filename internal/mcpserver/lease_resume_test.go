package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/statestore"
)

func TestLeaseResumeTransportReplayAndStaleReceipt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := statestore.Open(filepath.Join(root, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	m, err := jobs.New(filepath.Join(root, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(0)
	server := New(m, filesystem.New(p))
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "resume-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	decode := func(result *mcp.CallToolResult) projectlease.AcquireResult {
		t.Helper()
		if result.IsError {
			t.Fatal(result.Content)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var holder projectlease.AcquireResult
		if err := json.Unmarshal(raw, &holder); err != nil {
			t.Fatal(err)
		}
		return holder
	}
	old := decode(call("project_lease_acquire", map[string]any{"path": root, "owner": "first-chat", "idempotency_key": "acquire"}))
	args := map[string]any{"path": root, "token": old.Token, "owner": "second-chat", "idempotency_key": "resume"}
	next := decode(call("project_lease_resume", args))
	replay := decode(call("project_lease_resume", args))
	if next.Token == old.Token || next.Token != replay.Token || next.Status.Generation != 2 {
		t.Fatal("invalid resumed holder")
	}
	if out := call("project_lease_release", map[string]any{"path": root, "token": next.Token, "idempotency_key": "release"}); out.IsError {
		t.Fatal(out.Content)
	}
	if out := call("project_lease_resume", args); !out.IsError {
		t.Fatal("cached receipt resurrected successor")
	}
}
