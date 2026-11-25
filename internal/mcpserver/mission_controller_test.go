package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/gitops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/mission"
	"mcpshka/internal/statestore"
	missiontools "mcpshka/internal/tools/mission"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissionControllerToolsLifecycleAndReplay(t *testing.T) {
	m, _, in := yieldFixture(t)
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	controller := mission.NewController(db, gitops.New(m.Policy()), func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		calls = append(calls, name)
		switch name {
		case "capabilities_bootstrap":
			return map[string]any{"revision": strings.Repeat("a", 64)}, nil
		case "tool_resolve":
			return map[string]any{"code": "OK", "revision": strings.Repeat("a", 64), "candidates": []any{map[string]any{"name": args["goal"], "permission_granted": true, "input_schema": map[string]any{"type": "object"}}}}, nil
		case "capability_preflight":
			return map[string]any{"verdict": "ready"}, nil
		case "acceptance_status":
			return map[string]any{"done": false}, nil
		case "project_context":
			return map[string]any{}, nil
		case "project_lease_status":
			return map[string]any{"held": false}, nil
		default:
			t.Fatalf("unexpected effect: %s", name)
			return nil, nil
		}
	})
	server := mcp.NewServer(&mcp.Implementation{Name: "missions", Version: "1"}, nil)
	store := idempotency.New(t.TempDir())
	r := &toolRegistry{server: server, store: store, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	reader := missiontools.New(missiontools.Deps{Manager: m, Tasks: t.TempDir(), Runs: t.TempDir(), Projects: filepath.Dir(in.Path), Unit: func(context.Context, string) string { return "active" }})
	registerMissionTools(r, reader, controller)
	registerMissionControllerTools(r, controller, store)
	r.validate()
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any, fail bool) map[string]any {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError != fail {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
		out := map[string]any{}
		if !fail {
			raw, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	plan := map[string]any{"path": in.Path, "goal": "close P1", "mode": "CLOSE_STEP", "step": "P1", "idempotency_key": "start"}
	call("mission_preflight", map[string]any{"path": in.Path, "goal": "close P1", "mode": "CLOSE_STEP", "step": "P1"}, false)
	started := call("mission_start", plan, false)
	id, ok := started["id"].(string)
	if !ok || id == "" || started["agents_allowed"] == true {
		t.Fatal(started)
	}
	if call("mission_start", plan, false)["id"] != id {
		t.Fatal("start replay changed identity")
	}
	status := call("mission_status", map[string]any{"id": id}, false)
	if status["mission"].(map[string]any)["id"] != id {
		t.Fatal(status)
	}
	if len(call("mission_status", map[string]any{}, false)["missions"].([]any)) != 1 {
		t.Fatal("lost mission index")
	}
	pause := map[string]any{"id": id, "idempotency_key": "pause"}
	if call("mission_pause", pause, false)["state"] != "PAUSED" {
		t.Fatal("pause")
	}
	if call("mission_resume", map[string]any{"id": id, "idempotency_key": "resume"}, false)["state"] != "READY" {
		t.Fatal("resume")
	}
	// Replaying an old pause must not pause the resumed mission again.
	call("mission_pause", pause, false)
	if call("mission_status", map[string]any{"id": id}, false)["mission"].(map[string]any)["state"] != "READY" {
		t.Fatal("replay performed another transition")
	}
	report := call("mission_report", map[string]any{"id": id}, false)
	if report["git_head"] == "" || report["production"] != "UNKNOWN" {
		t.Fatal(report)
	}
	if call("mission_cancel", map[string]any{"id": id, "idempotency_key": "cancel"}, false)["state"] != "CANCELLED" {
		t.Fatal("cancel")
	}
	call("mission_resume", map[string]any{"id": id, "idempotency_key": "after-cancel"}, true)
	call("mission_pause", map[string]any{"id": id}, true)
	denied := authn.WithIdentity(ctx, authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"mission_status"}})
	if _, err := r.readers["mission_report"](denied, map[string]any{"id": id}); err == nil {
		t.Fatal("missing scope accepted")
	}
	for _, name := range calls {
		if name != "capabilities_bootstrap" && name != "tool_resolve" && name != "capability_preflight" && name != "acceptance_status" && name != "project_context" && name != "project_lease_status" {
			t.Fatal("effect delegated without consent", name)
		}
	}
}
