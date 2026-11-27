package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/idempotency"
	"mcpshka/internal/projectlease"
)

// A lost recovery response leaves the receipt PENDING. The same key re-runs
// the journal-replaying backend and returns the successor instead of UNKNOWN.
func TestReplayPendingContractCompletesLostRecoveryResponse(t *testing.T) {
	if !mutationContracts["project_lease_admin_recover"].ReplayPending || mutationContracts["service_action"].ReplayPending {
		t.Fatal("replay must stay limited to journal-replaying recovery")
	}
	store := idempotency.New(t.TempDir())
	server := mcp.NewServer(&mcp.Implementation{Name: "contract-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: store, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	status := projectlease.Status{Held: true, ProjectRoot: "/root/repos/mcpshka", LeaseID: strings.Repeat("a", 32), Generation: 2, ExecutionID: strings.Repeat("b", 32), Owner: "operator-root:fixture"}
	activeSession := true
	r.readers["project_lease_admin_recovery_status"] = func(context.Context, map[string]any) (any, error) {
		if !activeSession {
			return nil, errors.New("Face ID administrative session expired")
		}
		return status, nil
	}
	calls := 0
	addTool(r, &mcp.Tool{Name: "project_lease_admin_recover", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(context.Context, *mcp.CallToolRequest, AdminLegacyLeaseRecoverInput) (*mcp.CallToolResult, projectlease.AcquireResult, error) {
		calls++
		if calls == 1 {
			return nil, projectlease.AcquireResult{}, errors.New("lease recovery worker outcome UNKNOWN")
		}
		return nil, projectlease.AcquireResult{Token: "successor", Status: status}, nil
	})
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
	args := map[string]any{"path": "/root/repos/mcpshka", "session_id": "s", "idempotency_key": "recover-once",
		"plan": map[string]any{"Path": "/root/repos/mcpshka", "FenceRoot": "", "GitCommonDir": "", "LeaseID": "", "Generation": 0,
			"ExecutionID": "", "Owner": "", "RecordSHA256": "", "SnapshotTree": "", "JournalID": "", "Authorization": "", "NewOwner": ""}}
	for i, wantErr := range []bool{true, false, false} {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_lease_admin_recover", Arguments: args})
		if err != nil || result.IsError != wantErr {
			t.Fatalf("call %d: err=%v result=%+v", i, err, result)
		}
		if !wantErr {
			out, _ := result.StructuredContent.(map[string]any)
			if out["token"] != "successor" {
				t.Fatalf("call %d lost the successor: %+v", i, result.StructuredContent)
			}
		}
	}
	if calls != 2 {
		t.Fatalf("handler calls=%d, want one replay then a cached receipt", calls)
	}
	status.ExecutionID = strings.Repeat("c", 32)
	stale, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_lease_admin_recover", Arguments: args})
	if err != nil || !stale.IsError {
		t.Fatal("cached receipt accepted changed broker successor", err)
	}
	status.ExecutionID = strings.Repeat("b", 32)
	activeSession = false
	expired, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_lease_admin_recover", Arguments: args})
	if err != nil || !expired.IsError {
		t.Fatal("cached receipt bypassed expired Face ID", err)
	}

}
