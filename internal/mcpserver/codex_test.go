package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
	"testing"
)

func TestCodexMutationEvidencePinsLostReplyIdentity(t *testing.T) {
	var observed map[string]any
	r := &toolRegistry{readers: map[string]probeHandler{
		"codex_task_status": func(_ context.Context, in map[string]any) (any, error) {
			observed = in
			return map[string]any{"state": "unknown"}, nil
		},
	}}
	input := map[string]any{"session_id": "session_123", "idempotency_key": "task-once"}
	sum := sha256.Sum256([]byte("task-once"))
	for _, before := range []bool{true, false} {
		if _, err := r.evidence(context.Background(), "codex_task_submit", input, map[string]any{"turn_id": "turn_123"}, before); err != nil {
			t.Fatal(err)
		}
		if observed["session_id"] != "session_123" || observed["request_id"] != hex.EncodeToString(sum[:16]) {
			t.Fatalf("lost reply cannot be reconciled: %#v", observed)
		}
		if !before && observed["turn_id"] != "turn_123" {
			t.Fatal("accepted turn receipt was lost")
		}
	}
	if _, err := r.evidence(context.Background(), "codex_task_cancel", map[string]any{"session_id": "session_123", "queue_id": "queue_123"}, nil, true); err != nil {
		t.Fatal(err)
	}
	if observed["queue_id"] != "queue_123" {
		t.Fatal("cancel probe omitted its natural queue identity")
	}
}

func TestCodexCapacityToolUsesReadOnlyProbeAndRejectsFakeQuota(t *testing.T) {
	saved := codexRPC
	defer func() { codexRPC = saved }()
	calls := 0
	codexRPC = func(ctx context.Context, op string, input any) (map[string]any, error) {
		calls++
		if op != "task_status" {
			t.Fatalf("unexpected operation: %s", op)
		}
		return map[string]any{"state": "running", "quota_remaining": 900, "reset_at": "tomorrow"}, nil
	}
	store := idempotency.New(t.TempDir())
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	registry := &toolRegistry{server: server, store: store, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerCodexTools(registry, store)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_capacity_status", Arguments: map[string]any{"session_id": "session_123"}})
	if err != nil || out.IsError {
		t.Fatalf("capacity call: %v %+v", err, out)
	}
	b, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || data["quota_remaining"] != nil || data["reset_at"] != nil || data["handoff_required"] != false || data["turn_state"] != "running" {
		t.Fatalf("unsafe capacity output or calls=%d: %s", calls, b)
	}
	invalid, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_capacity_status", Arguments: map[string]any{"session_id": " "}})
	if err == nil && !invalid.IsError {
		t.Fatal("blank session ID was accepted")
	}
	if calls != 1 {
		t.Fatalf("invalid request invoked daemon: %d", calls)
	}
	codexRPC = func(ctx context.Context, op string, input any) (map[string]any, error) {
		calls++
		return map[string]any{"state": "quota_exhausted_please_release_lease", "quota_remaining": 1}, nil
	}
	unknown, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_capacity_status", Arguments: map[string]any{"session_id": "session_123"}})
	if err != nil || unknown.IsError {
		t.Fatalf("unknown state call: %v %+v", err, unknown)
	}
	b, err = json.Marshal(unknown.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	if data["turn_state"] != "unknown" || data["handoff_required"] != false || data["quota_remaining"] != nil {
		t.Fatalf("untrusted daemon state caused unsafe inference: %s", b)
	}
}

func TestCodexCapacityResponseDoesNotInventQuota(t *testing.T) {
	v := CodexCapacityStatus{Source: "codex_task_status", SessionID: "s", TurnState: "running", Confidence: "session-only; account-quota-unknown"}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["quota_remaining"] != nil || decoded["reset_at"] != nil || decoded["handoff_required"] != false {
		t.Fatalf("unexpected quota or handoff inference: %s", b)
	}
}

func TestCodexSubmitRetriesOnlyOnceWithoutRealWorker(t *testing.T) {
	saved := codexRPC
	defer func() { codexRPC = saved }()
	calls := 0
	codexRPC = func(ctx context.Context, op string, input any) (map[string]any, error) {
		if op == "task_submit" {
			calls++
		}
		return map[string]any{"queue_id": "queue_123", "state": "queued"}, nil
	}
	store := idempotency.New(t.TempDir())
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	registry := &toolRegistry{server: server, store: store, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerCodexTools(registry, store)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	args := map[string]any{"session_id": "session_123", "message": "inspect", "idempotency_key": "retry"}
	for range 2 {
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_task_submit", Arguments: args})
		if err != nil || out.IsError {
			t.Fatal("submit failed", err)
		}
	}
	if calls != 1 {
		t.Fatalf("duplicate submission calls=%d", calls)
	}
	args["message"] = "different"
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_task_submit", Arguments: args})
	if err != nil || !out.IsError {
		t.Fatal("retry conflict not rejected", err)
	}
}

func TestCodexTaskModelAndReasoningAreScopedToIdleReadOnlyTurn(t *testing.T) {
	saved := codexRPC
	defer func() { codexRPC = saved }()
	calls := 0
	badPayload := false
	codexRPC = func(_ context.Context, operation string, input any) (map[string]any, error) {
		if operation == "task_status" {
			return map[string]any{"state": "idle"}, nil
		}
		if operation != "task_submit" {
			badPayload = true
			return map[string]any{"state": "unknown"}, nil
		}
		calls++
		m, ok := input.(map[string]any)
		if !ok || m["mode"] != "start" || m["model"] != "sample-model" || m["reasoning_effort"] != "high" {
			badPayload = true
		}
		return map[string]any{"state": "queued"}, nil
	}
	store := idempotency.New(t.TempDir())
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	registry := &toolRegistry{server: server, store: store, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerCodexTools(registry, store)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	base := map[string]any{"session_id": "session_123", "message": "inspect only", "model": "sample-model", "reasoning_effort": "high", "idempotency_key": "r3-mock-turn"}
	for _, tc := range []struct {
		name   string
		change map[string]any
	}{
		{"queue_rejects_model", map[string]any{"mode": "queue"}},
		{"effort_requires_model", map[string]any{"mode": "start", "model": ""}},
		{"invalid_effort", map[string]any{"mode": "start", "reasoning_effort": "ultra-infinite"}},
		{"invalid_model", map[string]any{"mode": "start", "model": "../../bad"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{}
			for k, v := range base {
				args[k] = v
			}
			for k, v := range tc.change {
				args[k] = v
			}
			args["idempotency_key"] = "r3-invalid-" + tc.name
			out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_task_submit", Arguments: args})
			if err == nil && !out.IsError {
				t.Fatalf("unsafe request accepted: %#v", args)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid settings invoked daemon %d times", calls)
	}
	for i := 0; i < 2; i++ {
		args := map[string]any{}
		for k, v := range base {
			args[k] = v
		}
		args["mode"] = "start"
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_task_submit", Arguments: args})
		if err != nil || out.IsError {
			t.Fatalf("valid scoped request %d: %v", i, err)
		}
	}
	if calls != 1 || badPayload {
		t.Fatalf("idempotency or scoped model payload mismatch: calls=%d bad=%v", calls, badPayload)
	}
}

func TestCodexCapacityReadsBoundSessionStatus(t *testing.T) {
	saved := codexRPC
	defer func() { codexRPC = saved }()
	cases := []struct {
		name     string
		response map[string]any
		want     string
	}{
		{"active", map[string]any{"session": map[string]any{"session_id": "session_123", "status": map[string]any{"type": "active", "activeFlags": []any{"waitingOnApproval"}}}}, "active"},
		{"idle", map[string]any{"session": map[string]any{"session_id": "session_123", "status": map[string]any{"type": "idle"}}}, "idle"},
		{"not_loaded", map[string]any{"session": map[string]any{"session_id": "session_123", "status": map[string]any{"type": "notLoaded"}}}, "notLoaded"},
		{"system_error", map[string]any{"session": map[string]any{"session_id": "session_123", "status": map[string]any{"type": "systemError"}}}, "systemError"},
		{"wrong_session", map[string]any{"session": map[string]any{"session_id": "another_session", "status": map[string]any{"type": "idle"}}, "state": "completed"}, "unknown"},
		{"unknown_nested", map[string]any{"session": map[string]any{"session_id": "session_123", "status": map[string]any{"type": "quota_exhausted_release_lease"}}, "state": "idle"}, "unknown"},
		{"malformed_nested", map[string]any{"session": "invalid", "state": "idle"}, "unknown"},
		{"missing_identity", map[string]any{"session": map[string]any{"status": map[string]any{"type": "idle"}}, "state": "idle"}, "unknown"},
		{"malformed_status", map[string]any{"session": map[string]any{"session_id": "session_123", "status": "idle"}, "state": "idle"}, "unknown"},
		{"nested_over_legacy", map[string]any{"session": map[string]any{"session_id": "session_123", "status": map[string]any{"type": "active"}}, "state": "completed"}, "active"},
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	registry := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerCodexTools(registry, registry.store)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			codexRPC = func(ctx context.Context, op string, in any) (map[string]any, error) {
				calls++
				if op != "task_status" {
					t.Fatalf("unexpected operation %s", op)
				}
				return tc.response, nil
			}
			out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "codex_capacity_status", Arguments: map[string]any{"session_id": "session_123"}})
			if err != nil || out.IsError {
				t.Fatalf("capacity: %v %+v", err, out)
			}
			raw, err := json.Marshal(out.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			if err = json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || data["turn_state"] != tc.want || data["quota_remaining"] != nil || data["reset_at"] != nil || data["handoff_required"] != false {
				t.Fatalf("unsafe/unusable status %s; want %s", raw, tc.want)
			}
		})
	}
}
