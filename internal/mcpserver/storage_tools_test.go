package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledge"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func TestStorageSurfaceAndKnowledgeRevisionTools(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	statestore.Open(filepath.Join(state, "state.db"))
	os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600)
	m, e := jobs.New(filepath.Join(state, "jobs"), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close(time.Second)
	a, b := mcp.NewInMemoryTransports()
	ss, e := New(m, nil).Connect(context.Background(), a, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "storage-test", Version: "1"}, nil).Connect(context.Background(), b, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	listed, e := cs.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"storage_providers", "storage_auth_status", "storage_list", "storage_stat", "storage_mkdir", "storage_upload", "storage_download", "storage_move", "storage_trash", "storage_transfer_status", "backup_plan", "backup_create", "backup_status", "backup_verify", "backup_restore_plan", "knowledge_local_read", "knowledge_import_chat"} {
		if !names[name] && !hiddenTool(name) { // 0.14.0: hidden ones answer by name below
			t.Fatalf("missing tool %s", name)
		}
	}
	call := func(name string, in any) *mcp.CallToolResult {
		t.Helper()
		out, e := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: in})
		if e != nil || out.IsError {
			t.Fatalf("%s: %v %+v", name, e, out)
		}
		return out
	}
	r := knowledge.Record{ID: "article", Kind: "decision", Source: "owner", Scope: "mcpshka", Provenance: "test", Timestamp: time.Now().UTC(), Fields: map[string]knowledge.Field{"content": {Value: "private article"}, "password": {Value: "NEVER_LEAK", Secret: true}}}
	call("knowledge_write", KnowledgeWriteInput{Action: "add", Record: &r, IdempotencyKey: "article-add"})
	raw, _ := json.Marshal(call("knowledge_local_read", KnowledgeLocalInput{ID: r.ID, History: true}))
	if !strings.Contains(string(raw), "private article") || strings.Contains(string(raw), "NEVER_LEAK") {
		t.Fatal("local read privacy boundary")
	}
	r.Fields["content"] = knowledge.Field{Value: "corrected"}
	call("knowledge_write", KnowledgeWriteInput{Action: "update", ID: r.ID, Record: &r, Reason: "verified correction", ExpectedRevision: 1, IdempotencyKey: "article-update"})
	priorRaw, _ := json.Marshal(call("knowledge_local_read", KnowledgeLocalInput{ID: r.ID, Revision: 1}))
	if !strings.Contains(string(priorRaw), "private article") || strings.Contains(string(priorRaw), "NEVER_LEAK") {
		t.Fatal("revision-specific read failed")
	}
	call("knowledge_write", KnowledgeWriteInput{Action: "archive", ID: r.ID, Reason: "obsolete", ExpectedRevision: 2, IdempotencyKey: "article-archive"})
	raw, _ = json.Marshal(call("knowledge_local_read", KnowledgeLocalInput{ID: r.ID, History: true}))
	if !strings.Contains(string(raw), "verified correction") || !strings.Contains(string(raw), "archived") || strings.Contains(string(raw), "NEVER_LEAK") {
		t.Fatal("history missing or leaked")
	}
}

func TestLocalKnowledgeReadMasksCredentialNamedFields(t *testing.T) {
	r := knowledge.Record{Fields: map[string]knowledge.Field{"api_key": {Value: "DO_NOT_RETURN"}, "content": {Value: `{"password":"never show this"}`}}}
	raw, _ := json.Marshal(redactLocal(r, false))
	if strings.Contains(string(raw), "DO_NOT_RETURN") || strings.Contains(string(raw), "never show this") {
		t.Fatal("credential leaked in local read")
	}
}
