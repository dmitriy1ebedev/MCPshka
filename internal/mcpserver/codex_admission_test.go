package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
)

func admissionMockTask(id string) CodexAdmissionTaskInput {
	return CodexAdmissionTaskInput{
		ID: id, SessionID: "session_" + id + "testing", Worktree: "/tmp/r3-" + id,
		RoadmapSHA: strings.Repeat("a", 64), Message: "isolated review " + id, Model: "mock-model",
		ReasoningEffort: "high", Revision: 1, CatalogVerified: true, OwnerVerified: true,
		SessionIdle: true, WriterLeaseFree: true, ControlPathHealthy: true, QuotaVerified: true,
		QuotaAvailable: true, WallSeconds: 120, CPUPercent: 50, MemoryMiB: 128, OutputKiB: 32,
	}
}

func TestCodexAdmissionMCPToolReadOnlyOffline(t *testing.T) {
	saved := codexRPC
	defer func() { codexRPC = saved }()
	codexRPC = func(context.Context, string, any) (map[string]any, error) {
		t.Fatal("Codex RPC called by preview")
		return nil, nil
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "offline", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerCodexAdmissionTools(r)
	a := admissionMockTask("alpha")
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, e := server.Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "offline-test", Version: "1"}, nil)
	cs, e := client.Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	input := CodexAdmissionPreviewInput{Tasks: []CodexAdmissionTaskInput{a},
		Models:        []CodexAdmissionModelInput{{ID: "mock-model", Efforts: []string{"high"}}},
		MaxConcurrent: 1}
	raw, e := json.Marshal(input)
	if e != nil {
		t.Fatal(e)
	}
	var args map[string]any
	if e = json.Unmarshal(raw, &args); e != nil {
		t.Fatal(e)
	}
	result, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_admission_preview", Arguments: args})
	if e != nil || result.IsError {
		t.Fatal(result, e)
	}
	encoded, e := json.Marshal(result.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]any
	if e = json.Unmarshal(encoded, &out); e != nil {
		t.Fatal(e)
	}
	if out["trust"] != "UNTRUSTED" || out["dispatch_enabled"] != false ||
		out["source"] != "caller_supplied_offline_simulation" {
		t.Fatal(out)
	}
	decisions, ok := out["decisions"].([]any)
	if !ok || len(decisions) != 1 {
		t.Fatal(out)
	}
	d := decisions[0].(map[string]any)
	if d["status"] != "READY_FOR_APPROVAL" || d["dispatch_enabled"] != false {
		t.Fatal(out)
	}
}
