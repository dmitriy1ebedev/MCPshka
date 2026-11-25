package mcpserver

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func parityClient(t *testing.T, s *mcp.Server, owner string) *mcp.ClientSession {
	t.Helper()
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: owner, Role: "operator", Scopes: []string{"*"}})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "parity", Version: "1"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}
func parityCall(t *testing.T, c *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	return catalogCall(t, c, name, args)
}
func parityDenied(t *testing.T, c *mcp.ClientSession, name string, args map[string]any) {
	t.Helper()
	out, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err == nil && !out.IsError {
		t.Fatalf("%s unexpectedly allowed: %+v", name, out)
	}
}
func TestProcessParityThroughMCP(t *testing.T) {
	dir := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{dir}}
	m, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	s := New(m, filesystem.New(p))
	a, b := parityClient(t, s, "a"), parityClient(t, s, "b")
	start := map[string]any{"command": "cat", "cwd": dir, "idempotency_key": "same-key"}
	first := parityCall(t, a, "proc_start", start)
	id := first["session_id"].(string)
	if again := parityCall(t, a, "proc_start", start); again["session_id"] != id {
		t.Fatal("start replay launched duplicate")
	}
	other := parityCall(t, b, "proc_start", start)
	if other["session_id"] == id {
		t.Fatal("foreign receipt leaked session")
	}
	for _, tool := range []string{"proc_read", "proc_send", "proc_kill"} {
		parityDenied(t, b, tool, map[string]any{"session_id": id, "input": "bad", "idempotency_key": "bad-" + tool})
	}
	for _, tool := range []string{"job_status", "job_wait", "job_output", "job_cancel"} {
		parityDenied(t, b, tool, map[string]any{"job_id": id})
	}
	send := map[string]any{"session_id": id, "input": "hello\n", "timeout_ms": 1000, "pattern": "hello", "idempotency_key": "send-once"}
	out := parityCall(t, a, "proc_send", send)
	if !strings.Contains(out["text"].(string), "hello") {
		t.Fatal(out)
	}
	parityCall(t, a, "proc_send", send)
	out = parityCall(t, a, "proc_read", map[string]any{"session_id": id})
	if out["text"] != "hello\n" {
		t.Fatal("input replayed", out)
	}
	list := parityCall(t, a, "proc_list", map[string]any{})
	if len(list["sessions"].([]any)) != 1 {
		t.Fatal(list)
	}
	parityCall(t, a, "proc_kill", map[string]any{"session_id": id})
}
func TestSearchParityThroughMCP(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("needle\nneedle\n"), 0600)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("needle\n"), 0600)
	p := &policy.Policy{AllowedWorkdirs: []string{dir}}
	m, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	s := New(m, filesystem.New(p))
	a, b := parityClient(t, s, "a"), parityClient(t, s, "b")
	start := parityCall(t, a, "search_start", map[string]any{"path": dir, "pattern": "needle", "glob": "*.go", "max_results": 1, "idempotency_key": "search-one"})
	id := start["id"].(string)
	parityDenied(t, b, "search_more", map[string]any{"id": id})
	own := parityCall(t, a, "list_searches", nil)["sessions"].([]any)
	foreign := parityCall(t, b, "list_searches", nil)["sessions"].([]any)
	if len(own) != 1 || own[0] != id || len(foreign) != 0 {
		t.Fatal(own, foreign)
	}
	out := parityCall(t, a, "search_more", map[string]any{"id": id})
	matches := out["matches"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["line"] != float64(1) {
		t.Fatal(out)
	}
	cursor := out["cursor"].(string)
	out = parityCall(t, a, "search_more", map[string]any{"id": id, "cursor": cursor})
	if out["matches"].([]any)[0].(map[string]any)["line"] != float64(2) {
		t.Fatal(out)
	}
	parityCall(t, a, "search_stop", map[string]any{"id": id})
	if got := parityCall(t, a, "list_searches", nil)["sessions"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	parityDenied(t, a, "search_more", map[string]any{"id": id})
}
