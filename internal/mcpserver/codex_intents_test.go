package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/codexcoord"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/statestore"
)

func TestCodexIntentAPIOfflineNoCodexCalls(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	original := codexRPC
	defer func() { codexRPC = original }()
	codexRPC = func(context.Context, string, any) (map[string]any, error) {
		t.Fatal("Codex must not be invoked by intent API")
		return nil, nil
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "offline", Version: "1"}, nil)
	registry := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerCodexIntentTools(registry, db)
	registerCodexQueueTools(registry, db)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, e := server.Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, e := client.Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil || result.IsError {
			t.Fatalf("%s: %+v %v", name, result, e)
		}
		raw, e := json.Marshal(result.StructuredContent)
		if e != nil {
			t.Fatal(e)
		}
		var out map[string]any
		if e = json.Unmarshal(raw, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	create := map[string]any{"id": "alpha", "session_id": "session_alpha123", "worktree": "/tmp/r3-alpha", "roadmap_sha256": strings.Repeat("a", 64), "message": "offline plan", "model": "fast-model", "reasoning_effort": "low", "idempotency_key": "create-alpha"}
	for i := 0; i < 2; i++ {
		v := call("codex_intent_create", create)
		if v["dispatch_enabled"] != false || v["catalog_verified"] != false || v["state"] != "draft" {
			t.Fatalf("unsafe create %v", v)
		}
	}
	v := call("codex_intent_status", map[string]any{"id": "alpha"})
	if v["found"] != true || v["dispatch_enabled"] != false {
		t.Fatal(v)
	}
	missing := call("codex_intent_status", map[string]any{"id": "absent"})
	if missing["found"] != false {
		t.Fatal(missing)
	}
	changed := call("codex_intent_update", map[string]any{"id": "alpha", "expected_revision": 1, "model": "deep-model", "reasoning_effort": "high", "idempotency_key": "change-alpha"})
	if changed["revision"] != float64(2) || changed["model"] != "deep-model" {
		t.Fatal(changed)
	}
	listed := call("codex_intent_list", map[string]any{"limit": 2})
	if listed["dispatch_enabled"] != false || len(listed["intents"].([]any)) != 1 {
		t.Fatal(listed)
	}
	cancel := call("codex_intent_cancel", map[string]any{"id": "alpha", "expected_revision": 2, "idempotency_key": "cancel-alpha"})
	if cancel["state"] != "cancelled" || cancel["dispatch_enabled"] != false {
		t.Fatal(cancel)
	}

	// A supervisor can stop after claiming the session, before the task CAS.
	q, e := codexcoord.New(db)
	if e != nil {
		t.Fatal(e)
	}
	task := codexcoord.Task{ID: "partial", Provider: "codex", SessionID: "session_partial123",
		Worktree: "/tmp/r3-partial", RoadmapSHA: strings.Repeat("b", 64),
		Message: "private dispatch context", Model: "fast-model", Effort: "low"}
	if _, e = q.Enqueue(task, []codexcoord.Model{{ID: "fast-model", Efforts: []string{"low"}}}); e != nil {
		t.Fatal(e)
	}
	clean := call("codex_queue_status", map[string]any{"id": task.ID})
	if clean["next_safe_action"] != "catalog_and_owner_checks_required" {
		t.Fatal(clean)
	}
	if _, e = db.PutIfAbsent("codexcoord/claims/session/"+task.SessionID, []byte(task.ID+"\noriginal-dispatch")); e != nil {
		t.Fatal(e)
	}
	for _, state := range []string{"queued", "hold"} {
		if state == "hold" {
			if _, e = q.Suspend(task.ID, 1); e != nil {
				t.Fatal(e)
			}
		}
		status := call("codex_queue_status", map[string]any{"id": task.ID})
		if status["dispatch_enabled"] != false || status["next_safe_action"] != "reconcile_dispatch_claims_without_replay" {
			t.Fatalf("hidden unresolved dispatch: %v", status)
		}
		row := status["task"].(map[string]any)
		if row["state"] != state || row["dispatch_id"] != nil {
			t.Fatal(row)
		}
		raw, e := json.Marshal(status)
		if e != nil || strings.Contains(string(raw), task.Message) || strings.Contains(string(raw), "original-dispatch") {
			t.Fatalf("status leaked private context: %s %v", raw, e)
		}
	}
}
