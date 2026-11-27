package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeToolsColdAIAndForgetSearch(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	statestore.Open(filepath.Join(state, "state.db"))
	os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600)
	m, e := jobs.New(filepath.Join(state, "jobs"), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close(time.Second)
	st, ct := mcp.NewInMemoryTransports()
	ss, e := New(m, nil).Connect(context.Background(), st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "cold-ai", Version: "1"}, nil).Connect(context.Background(), ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, e := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil || r.IsError {
			raw, _ := json.Marshal(r)
			t.Fatalf("%s %v %s", name, e, raw)
		}
		return r
	}
	for _, item := range []struct{ id, value string }{{"router", "BE900"}, {"server", "H96"}} {
		call("knowledge_write", map[string]any{"action": "add", "idempotency_key": "add-" + item.id, "record": map[string]any{"id": item.id, "kind": "device", "source": "user-confirmed", "scope": "home", "provenance": "authorized-canary", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "export_metadata": true, "fields": map[string]any{"model": map[string]any{"value": item.value, "export": true}, "secret": map[string]any{"value": "NEVER_EXPORT_TOKEN", "export": true, "secret": true}}}})
	}
	// New session sees persistent infrastructure answers without chat history.
	for _, q := range []string{"BE900", "H96"} {
		r := call("knowledge_search", map[string]any{"query": map[string]any{"text": q, "limit": 5}, "max_records": 100, "max_bytes": 4096})
		raw, _ := json.Marshal(r)
		if !strings.Contains(string(raw), q) || strings.Contains(string(raw), "NEVER_EXPORT_TOKEN") {
			t.Fatal("cold answer or secret boundary")
		}
	}
	call("knowledge_write", map[string]any{"action": "forget", "id": "router", "idempotency_key": "forget-router"})
	r := call("knowledge_search", map[string]any{"query": map[string]any{"text": "BE900", "limit": 5}, "max_records": 100, "max_bytes": 4096})
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "BE900") {
		t.Fatal("forgotten answer retained")
	}
	call("index_health", map[string]any{})
}
