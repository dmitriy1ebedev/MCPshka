package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/authn"
	"mcpshka/internal/failure"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/toolcontract"
)

// A forge_ci call with a short SHA was refused by validation, yet its key
// stayed PENDING: the corrected retry got "already used for a different
// operation" (BUG-20261103-5). A no-effect refusal releases the key; any
// other error keeps it held.
func TestNoEffectRefusalReleasesRetryKey(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "noeffect-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	server.AddReceivingMiddleware(failureMetadata)
	r.readers["admin_session_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "active"}, nil }
	effects := 0
	addTool(r, &mcp.Tool{Name: "admin_session_end", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(_ context.Context, _ *mcp.CallToolRequest, in AdminSessionEndInput) (*mcp.CallToolResult, map[string]any, error) {
		switch in.SessionID {
		case "bad":
			return nil, nil, failure.Rejected("TEST_INPUT", "test", "input rejected before any effect", errors.New("bad session"))
		case "cache":
			e := failure.RejectedAs(failure.Transient, failure.GoCacheMaintenance, "jobs.admission", "Go cache maintenance in progress", nil)
			e.RetryAfterSeconds = 30
			return nil, nil, e
		case "boom":
			return nil, nil, errors.New("effect may have happened")
		}
		effects++
		return nil, map[string]any{"state": "closed"}, nil
	})
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner", Role: "operator", Scopes: []string{"admin_session_end"}})
	call := func(session, key string) *mcp.CallToolResult {
		t.Helper()
		st, ct := mcp.NewInMemoryTransports()
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
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "admin_session_end", Arguments: map[string]any{"session_id": session, "action": "close", "idempotency_key": key}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if r := call("bad", "k1"); !r.IsError {
		t.Fatal("rejection not reported")
	}
	if r := call("good", "k1"); r.IsError {
		content, _ := json.Marshal(r.Content)
		t.Fatalf("corrected retry with the same key refused: %s", content)
	}
	first := call("cache", "cache-key")
	replay := call("cache", "cache-key")
	if !first.IsError || !replay.IsError || !reflect.DeepEqual(first.StructuredContent, replay.StructuredContent) || mcpErrorText(first) != mcpErrorText(replay) {
		t.Fatalf("unstable refusal: %+v / %+v", first, replay)
	}
	envelope, _ := first.StructuredContent.(map[string]any)
	if envelope["actual_effect"] != "none" || envelope["failure_layer"] != "host" || envelope["retry_allowed"] != true || envelope["retry_after_seconds"] != float64(30) || envelope["receipt"] == nil {
		t.Fatalf("missing cache refusal evidence: %+v", envelope)
	}
	inputs := map[string]any{"session_id": "cache", "action": "close", "idempotency_key": "cache-key"}
	key := mutationReceiptKey(ctx, "admin_session_end", "cache-key", inputs)
	proof, err := toolcontract.ProbeMutation(ctx, r.store, toolcontract.MutationProbeInput{Tool: "admin_session_end", Key: "cache-key"}, toolcontract.ProbeOptions{ReceiptKey: key})
	if err != nil || proof["actual_effect"] != "none" || proof["retry_allowed"] != true || proof["outcome"] != "rejected_before_execution" {
		t.Fatalf("probe: %+v (%v)", proof, err)
	}
	st, _ := proof["receipt"].(idempotency.Status)
	if st.State != idempotency.Failed || st.FinishedAt.IsZero() {
		t.Fatalf("refusal is not terminal: %+v", st)
	}
	if effects != 1 {
		t.Fatalf("effects = %d", effects)
	}
	// An error without the no-effect proof keeps the key held (UNKNOWN).
	if r := call("boom", "k2"); !r.IsError {
		t.Fatal("error not reported")
	}
	if r := call("good", "k2"); !r.IsError {
		t.Fatal("key of an unproven failure was reused")
	}
}
