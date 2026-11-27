package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCodexRoutePreviewNeverDispatchesOrTrustsCallerEvidence(t *testing.T) {
	original := codexRPC
	defer func() { codexRPC = original }()
	codexRPC = func(context.Context, string, any) (map[string]any, error) {
		t.Fatal("route preview attempted real Codex RPC")
		return nil, nil
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "offline-routing", Version: "1"}, nil)
	registry := &toolRegistry{server: s, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerCodexRoutePreview(registry)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	c := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "1"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	task := map[string]any{
		"task_id": "task123", "session_id": "session_r3_123",
		"model": "deep", "reasoning_effort": "high",
		"catalog": []any{
			map[string]any{"model": "deep", "supported_reasoning_efforts": []string{"medium", "high", "xhigh"}, "default_reasoning_effort": "medium"},
		},
		"turn": map[string]any{"session_id": "session_r3_123", "active": true, "turn_id": "turn_123",
			"ownership_verified": true, "writer_fenced": true, "live_update_negotiated": true, "explicit_authorization": true},
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_route_preview", Arguments: task})
	if err != nil || result.IsError {
		rawResult, _ := json.Marshal(result)
		t.Fatalf("read-only preview failed %v %s", err, rawResult)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if value["catalog_verified"] != false || value["dispatch_enabled"] != false || value["trust"] != "UNTRUSTED" {
		t.Fatalf("fabricated authority: %s", raw)
	}
	route, ok := value["switch"].(map[string]any)
	if !ok || route["action"] != "live_update_candidate" || route["dispatch_enabled"] != false {
		t.Fatalf("unsafe route: %s", raw)
	}

	// Bogus effort is rejected: no downgrade to the model default.
	task["reasoning_effort"] = "low"
	denied, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_route_preview", Arguments: task})
	if err == nil && !denied.IsError {
		t.Fatal("unsupported reasoning accepted")
	}
	task["reasoning_effort"] = "high"
	task["turn"] = map[string]any{"session_id": "session_r3_123", "active": true}
	held, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_route_preview", Arguments: task})
	if err != nil || held.IsError {
		t.Fatal("fenced HOLD preview rejected", err)
	}
	raw, _ = json.Marshal(held.StructuredContent)
	value = map[string]any{}
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	route, ok = value["switch"].(map[string]any)
	if !ok || route["action"] != "HOLD" || route["dispatch_enabled"] != false {
		t.Fatalf("foreign live turn not HOLD: %s", raw)
	}
}
