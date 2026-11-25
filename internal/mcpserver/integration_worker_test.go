package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/executor"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/integrationqueue"
	"strings"
	"testing"
	"time"
)

func TestIntegrationWorkerRejectsForeignOwnerBeforeBroker(t *testing.T) {
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "ours", Role: "operator", Scopes: []string{"*"}})
	for _, action := range []string{"launch", "fence", "recover", "abort"} {
		in := IntegrationWorkerInput{PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("b", 64), Action: action, IdempotencyKey: "once"}
		a := executor.IntegrationApproval{ID: in.ApprovalID, TestPin: executor.Pin{PermitID: in.PermitID}, Request: integrationqueue.Request{Owner: "foreign"}}
		called := false
		_, err := integrationWorkerWithApproval(ctx, nil, in, a, func(context.Context, string, string, string) ([]byte, error) { called = true; return nil, nil })
		if err == nil || called || !strings.Contains(err.Error(), "another authenticated owner") {
			t.Fatalf("%s: broker reached: %v", action, err)
		}
	}
}
func TestIntegrationWorkerRejectsUnpinnedRuntime(t *testing.T) {
	a := executor.IntegrationApproval{ID: strings.Repeat("b", 64), ExecutionID: strings.Repeat("c", 32), Repository: executor.RepositoryIdentity{CommonDir: "/root/repos/controlled.git", Device: 1, Inode: 2}, Request: integrationqueue.Request{Owner: "ours", Commit: strings.Repeat("d", 40)}}
	claim := executor.IntegrationClaim{Version: 1, ApprovalID: a.ID, Repository: a.Repository, ExecutionID: a.ExecutionID, Candidate: a.Request.Commit, Owner: a.Request.Owner, Generation: 2, CreatedAt: time.Now(), State: "HELD"}
	pin := executor.Pin{PermitID: strings.Repeat("e", 32), LeaseID: strings.Repeat("f", 32), Generation: 2, ExecutionID: a.ExecutionID}
	launch := executor.IntegrationLaunch{Version: 1, ApprovalID: a.ID, Claim: claim, Pin: pin, LaunchID: strings.Repeat("1", 32), State: "READY"}
	state := executor.IntegrationRuntime{Launch: launch, Claim: claim, Dispatched: true, Worker: executor.Snapshot{Pin: pin, State: "ACTIVE", Launches: []executor.Launch{{ID: launch.LaunchID, State: "LAUNCH_INTENT"}}}}
	raw, _ := json.Marshal(state)
	if _, err := decodeIntegrationWorkerOutput(raw, "launch", a); err != nil {
		t.Fatal(err)
	}
	for _, mut := range []func(*executor.IntegrationRuntime){
		func(v *executor.IntegrationRuntime) { v.Claim.Candidate = strings.Repeat("2", 40) },
		func(v *executor.IntegrationRuntime) {
			v.Launch.Pin.ExecutionID = strings.Repeat("2", 32)
			v.Worker.Pin = v.Launch.Pin
		},
		func(v *executor.IntegrationRuntime) { v.Launch.Claim.Generation++ },
		func(v *executor.IntegrationRuntime) { v.Worker.Launches[0].ID = strings.Repeat("3", 32) },
		func(v *executor.IntegrationRuntime) { v.Dispatched = false },
	} {
		var altered executor.IntegrationRuntime
		json.Unmarshal(raw, &altered)
		mut(&altered)
		bad, _ := json.Marshal(altered)
		if _, err := decodeIntegrationWorkerOutput(bad, "launch", a); err == nil {
			t.Fatal("accepted unpinned runtime")
		}
	}
	if _, err := decodeIntegrationWorkerOutput(raw, "fence", a); err == nil {
		t.Fatal("ACTIVE treated as fenced")
	}
	if _, err := decodeIntegrationWorkerOutput(append(raw, []byte("{}")...), "status", a); err == nil {
		t.Fatal("trailing root payload accepted")
	}
	claim.State = "RELEASED"
	bad, _ := json.Marshal(claim)
	if _, err := decodeIntegrationWorkerOutput(bad, "abort", a); err == nil {
		t.Fatal("successful completion treated as failure retirement")
	}
	claim.State = "FAILED"
	bad, _ = json.Marshal(claim)
	if _, err := decodeIntegrationWorkerOutput(bad, "abort", a); err != nil {
		t.Fatal(err)
	}
}
func TestIntegrationWorkerCompatUsesSealedEvidence(t *testing.T) {
	for _, entry := range []struct{ action, probe string }{{"launch", "integration_approval_status"}, {"fence", "integration_worker_status"}, {"recover", "integration_worker_status"}, {"abort", "integration_recovery_status"}} {
		v := IntegrationWorkerInput{PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("b", 64), Action: entry.action, Workdir: "/root/repos/source", IdempotencyKey: "once"}
		raw, _ := json.Marshal(v)
		input, _ := object(ExecStartInput{Command: "mcpshka", Args: []string{"integration-worker", string(raw)}, Workdir: v.Workdir, IdempotencyKey: "once"})
		r := &toolRegistry{readers: map[string]probeHandler{entry.probe: func(_ context.Context, args map[string]any) (any, error) {
			if args["permit_id"] != v.PermitID || args["approval_id"] != v.ApprovalID || args["workdir"] != v.Workdir {
				t.Fatal("lost sealed pins")
			}
			return map[string]any{"state": "HELD"}, nil
		}}}
		for _, before := range []bool{true, false} {
			ev, err := r.evidence(context.Background(), "exec_start", input, map[string]any{"id": "transport"}, before)
			if err != nil || ev.Probe != entry.probe {
				t.Fatalf("%s: %v", entry.action, err)
			}
		}
	}
}
func TestIntegrationWorkerCompatCompletedReceiptRechecksOwnerAndScope(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "recovery-contract-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	effects := 0
	r.readers["state_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "ready"}, nil }
	r.readers["integration_worker_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "RELEASED"}, nil }
	addTool(r, &mcp.Tool{Name: "exec_start", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, in ExecStartInput) (*mcp.CallToolResult, map[string]any, error) {
		if uploadOwner(ctx) != "controller-a" {
			return nil, nil, errors.New("integration recovery belongs to another authenticated owner: HOLD")
		}
		effects++
		return nil, map[string]any{"id": "receipt", "state": "done"}, nil
	})
	request, _ := json.Marshal(IntegrationWorkerInput{Action: "fence", PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("b", 64), Workdir: "/root/repos/test-source", IdempotencyKey: "once"})
	call := func(owner string, scopes []string) *mcp.CallToolResult {
		t.Helper()
		ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: owner, Role: "operator", Scopes: scopes})
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
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "exec_start", Arguments: map[string]any{"command": "mcpshka", "args": []string{"integration-worker", string(request)}, "workdir": "/root/repos/test-source", "idempotency_key": "once"}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	scopes := []string{"exec_start", "integration_worker_fence", "state_status", "integration_worker_status"}
	for range 2 {
		if out := call("controller-a", scopes); out.IsError {
			t.Fatalf("completed receipt unavailable: %+v", out)
		}
	}
	if effects != 1 {
		t.Fatalf("completed effect repeated: %d", effects)
	}
	if out := call("controller-b", scopes); !out.IsError {
		t.Fatal("foreign owner received completed receipt")
	}
	if out := call("controller-a", []string{"exec_start", "state_status", "integration_worker_status"}); !out.IsError {
		t.Fatal("revoked recovery scope received cached receipt")
	}
	if effects != 1 {
		t.Fatalf("failed retry repeated effect: %d", effects)
	}
}
