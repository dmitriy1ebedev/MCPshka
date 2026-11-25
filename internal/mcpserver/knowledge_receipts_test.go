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

func TestCachedKnowledgeReceiptsDoNotRetainForgottenFacts(t *testing.T) {
	ctx := context.Background()
	root, state := t.TempDir(), t.TempDir()
	db, e := statestore.Open(filepath.Join(state, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	m, e := jobs.New(filepath.Join(state, "jobs"), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close(time.Second)
	st, ct := mcp.NewInMemoryTransports()
	ss, e := New(m, nil).Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "cached-reader", Version: "1"}, nil).Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	call := func(action, key string, p any) jobs.Snapshot {
		t.Helper()
		raw, e := json.Marshal(p)
		if e != nil {
			t.Fatal(e)
		}
		r, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "exec_start", Arguments: map[string]any{"command": "mcpshka", "args": []string{"knowledge", action, string(raw)}, "workdir": root, "idempotency_key": key}})
		if e != nil || r.IsError {
			t.Fatalf("%s: %v %+v", action, e, r)
		}
		raw, e = json.Marshal(r.StructuredContent)
		if e != nil {
			t.Fatal(e)
		}
		var out jobs.Snapshot
		if e = json.Unmarshal(raw, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	record := map[string]any{"id": "owned-fact", "kind": "device", "source": "user", "scope": "home", "provenance": "canary", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "export_metadata": true, "fields": map[string]any{"model": map[string]any{"value": "FORGETTABLE_MODEL", "export": true}, "secret": map[string]any{"value": "CREDENTIAL_PRIVATE_MARKER", "secret": true, "export": true}}}
	write := call("write", "cached-add", map[string]any{"action": "add", "record": record})
	if strings.Contains(strings.Join(write.Args, " "), "CREDENTIAL_PRIVATE_MARKER") {
		t.Fatal("request retained in argv")
	}
	read := call("search", "cached-search", map[string]any{"query": map[string]any{"text": "FORGETTABLE_MODEL", "limit": 5}, "max_records": 100, "max_bytes": 4096})
	output, e := m.Read(read.ID, "stdout", 0, 4096)
	if e != nil || !strings.Contains(output.Text, "FORGETTABLE_MODEL") || strings.Contains(output.Text, "CREDENTIAL_PRIVATE_MARKER") {
		t.Fatal("visible output", e)
	}
	persisted, e := os.ReadFile(filepath.Join(m.StateDir(), read.ID, "stdout.log"))
	if e != nil || len(persisted) != 0 {
		t.Fatal("facts written to disk")
	}
	keys, e := db.List("")
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range keys {
		if key == "knowledge/v1/records" {
			continue
		}
		raw, e := db.Get(key)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(raw), "CREDENTIAL_PRIVATE_MARKER") || strings.Contains(string(raw), "FORGETTABLE_MODEL") {
			t.Fatalf("facts retained outside canonical store: %s", key)
		}
	}
	call("write", "cached-forget", map[string]any{"action": "forget", "id": "owned-fact"})
	if _, e = m.Read(read.ID, "stdout", 0, 4096); e == nil {
		t.Fatal("forgotten evidence still available")
	}
}
