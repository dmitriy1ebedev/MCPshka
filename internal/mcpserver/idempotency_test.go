package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/filesystem"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestExecStartIdempotencyPreventsDuplicateMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state", "jobs")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "side-effect.txt")
	script := "import sys; open(sys.argv[1], 'a').write('x\\n')"

	p := &policy.Policy{
		AllowedWorkdirs: []string{root},
		DeniedCommands:  map[string]struct{}{},
		// This isolated idempotency fixture intentionally exercises inline code;
		// production Default policy rejects ClassPrivileged without sandboxing.
		AllowedCommandClasses: map[policy.CommandClass]bool{policy.ClassReadOnly: true, policy.ClassMutating: true, policy.ClassPrivileged: true},
	}
	manager, err := jobs.New(stateDir, p)
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

	args := map[string]any{
		"command":         "python3",
		"args":            []string{"-c", script, target},
		"workdir":         root,
		"timeout_seconds": 30,
		"idempotency_key": "retry-once",
	}
	first := callSnapshot(t, ctx, clientSession, args)
	second := callSnapshot(t, ctx, clientSession, args)
	if first.ID == "" || second.ID != first.ID {
		t.Fatalf("job ids differ: first=%q second=%q", first.ID, second.ID)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, err := manager.Get(first.ID)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == jobs.Done {
			break
		}
		if s.State == jobs.Failed || s.State == jobs.Cancelled || s.State == jobs.TimedOut {
			t.Fatalf("job ended %s: %s", s.State, s.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "x\n" {
		t.Fatalf("side effect occurred more than once or not at all: %q", data)
	}

	statusRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "idempotency_status",
		Arguments: map[string]any{"key": "retry-once"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if statusRes.IsError {
		t.Fatalf("idempotency_status failed: %+v", statusRes.Content)
	}
	b, err := json.Marshal(statusRes.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var status idempotency.Status
	if err := json.Unmarshal(b, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != idempotency.Completed || status.Operation != "exec_start" {
		t.Fatalf("unexpected status: %+v", status)
	}

	conflictArgs := map[string]any{
		"command":         "python3",
		"args":            []string{"-c", "print('different')"},
		"workdir":         root,
		"timeout_seconds": 30,
		"idempotency_key": "retry-once",
	}
	conflict, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "exec_start", Arguments: conflictArgs})
	if err == nil && !conflict.IsError {
		t.Fatalf("same idempotency key with different payload unexpectedly succeeded: %+v", conflict)
	}
}

func callSnapshot(t *testing.T, ctx context.Context, clientSession *mcp.ClientSession, args map[string]any) jobs.Snapshot {
	t.Helper()
	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "exec_start", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("exec_start tool error: %+v", res.Content)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out jobs.Snapshot
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
