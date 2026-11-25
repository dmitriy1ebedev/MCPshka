package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/executor"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestIntegrationRecoveryRejectsCallerAuthorityBeforeBroker(t *testing.T) {
	for _, in := range []IntegrationRecoveryInput{
		{PermitID: "../state", ApprovalID: strings.Repeat("a", 64), IdempotencyKey: "retry"},
		{PermitID: strings.Repeat("a", 32), ApprovalID: "true", IdempotencyKey: "retry"},
		{PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("A", 64), IdempotencyKey: "retry"},
		{PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("b", 64)},
	} {
		if _, err := integrationRecoveryCall(context.Background(), nil, in, false); err == nil {
			t.Fatalf("unsafe recovery accepted: %+v", in)
		}
	}
}

func TestIntegrationRecoveryCompatEvidenceUsesRootClaim(t *testing.T) {
	request := IntegrationRecoveryInput{PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("b", 64), Workdir: "/root/repos/test-source", IdempotencyKey: "once"}
	raw, _ := json.Marshal(request)
	input, _ := object(ExecStartInput{Command: "mcpshka", Args: []string{"integration-recover", string(raw)}, Workdir: request.Workdir, IdempotencyKey: request.IdempotencyKey})
	calls := 0
	r := &toolRegistry{readers: map[string]probeHandler{
		"integration_recovery_status": func(_ context.Context, args map[string]any) (any, error) {
			calls++
			if args["permit_id"] != request.PermitID || args["approval_id"] != request.ApprovalID || args["workdir"] != request.Workdir {
				t.Fatalf("root claim probe lost fixed pins: %v", args)
			}
			return map[string]any{"state": "EFFECT_INTENT"}, nil
		},
	}}
	for _, before := range []bool{true, false} {
		evidence, err := r.evidence(context.Background(), "exec_start", input, map[string]any{"id": "transport-receipt"}, before)
		if err != nil || evidence.Probe != "integration_recovery_status" || evidence.Snapshot["state"] != "EFFECT_INTENT" {
			t.Fatalf("transport receipt substituted for root integration claim: %+v %v", evidence, err)
		}
	}
	if calls != 2 {
		t.Fatalf("root claim not observed before and after: %d", calls)
	}
}

func TestIntegrationRecoveryCompatCompletedReceiptRechecksOwnerAndScope(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "recovery-contract-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	effects := 0
	r.readers["state_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "ready"}, nil }
	r.readers["integration_recovery_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "RELEASED"}, nil }
	addTool(r, &mcp.Tool{Name: "exec_start", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, in ExecStartInput) (*mcp.CallToolResult, map[string]any, error) {
		if uploadOwner(ctx) != "controller-a" {
			return nil, nil, errors.New("integration recovery belongs to another authenticated owner: HOLD")
		}
		effects++
		return nil, map[string]any{"id": "receipt", "state": "done"}, nil
	})
	request, _ := json.Marshal(IntegrationRecoveryInput{PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("b", 64), Workdir: "/root/repos/test-source", IdempotencyKey: "once"})
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
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "exec_start", Arguments: map[string]any{"command": "mcpshka", "args": []string{"integration-recover", string(request)}, "workdir": "/root/repos/test-source", "idempotency_key": "once"}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	scopes := []string{"exec_start", "integration_recover", "state_status", "integration_recovery_status"}
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
	if out := call("controller-a", []string{"exec_start", "state_status", "integration_recovery_status"}); !out.IsError {
		t.Fatal("revoked recovery scope received cached receipt")
	}
	if effects != 1 {
		t.Fatalf("failed retry repeated effect: %d", effects)
	}
}

func TestIntegrationRecoveryForeignOwnerNeverReachesMutatingBroker(t *testing.T) {
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "controller-a", Role: "operator", Scopes: []string{"*"}})
	in := IntegrationRecoveryInput{PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("b", 64), IdempotencyKey: "foreign-recovery"}
	a := executor.IntegrationApproval{ID: in.ApprovalID, TestPin: executor.Pin{PermitID: in.PermitID}, Request: integrationqueue.Request{Owner: "foreign-owner"}}
	called := false
	_, err := integrationRecoveryWithApproval(ctx, nil, in, false, a, func(context.Context, string, string, string) ([]byte, error) { called = true; return nil, nil })
	if err == nil || !strings.Contains(err.Error(), "another authenticated owner") || called {
		t.Fatal("foreign owner reached broker", err, called)
	}
}

func TestIntegrationRecoveryCompatTransportReachesHandlerAndRechecksScope(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(filepath.Join(state, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(time.Second)
	server := New(manager, filesystem.New(p))
	call := func(scopes []string) string {
		t.Helper()
		ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "controller-a", Role: "operator", Scopes: scopes})
		st, ct := mcp.NewInMemoryTransports()
		ss, err := server.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ss.Close()
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "recovery-test", Version: "1"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		request, _ := json.Marshal(IntegrationRecoveryInput{PermitID: "invalid-fixed-id", ApprovalID: strings.Repeat("a", 64), Workdir: root, IdempotencyKey: "transport-recovery"})
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "exec_start", Arguments: map[string]any{"command": "mcpshka", "args": []string{"integration-recover", string(request)}, "workdir": root, "idempotency_key": "transport-recovery"}})
		if err != nil {
			return err.Error()
		}
		if !result.IsError {
			t.Fatal("invalid permit accepted")
		}
		raw, _ := json.Marshal(result)
		return string(raw)
	}
	// A well-formed compatibility request must reach the recovery handler's
	// fixed-ID validation, without contacting the live broker in this fixture.
	got := call([]string{"exec_start", "integration_recover", "state_status", "integration_recovery_status"})
	if !strings.Contains(got, "integration recovery requires fixed permit") {
		t.Fatalf("compatibility handler unreachable: %s", got)
	}
	// Same authenticated owner and retry key, but recovery scope revoked:
	// authorization must run before even a cached failed receipt is consulted.
	got = call([]string{"exec_start", "state_status", "integration_recovery_status"})
	if !strings.Contains(got, "lacks scope") || !strings.Contains(got, "integration_recover") {
		t.Fatalf("cached retry bypassed recovery scope: %s", got)
	}
}

func TestIntegrationRecoveryCompatRejectsOverridesAndInputAuthority(t *testing.T) {
	for _, in := range []ExecStartInput{
		{Args: []string{"integration-recover", "{}", "true"}},
		{Args: []string{"integration-recover", "{}"}, Env: map[string]string{"ROOT": "true"}},
		{Args: []string{"integration-recover", "{}"}, ApprovalToken: "caller"},
		{Args: []string{"integration-recover", `{"permit_id":"x","success":true}`}},
	} {
		if _, err := integrationRecoveryCompat(context.Background(), nil, in); err == nil {
			t.Fatal("recovery override accepted")
		}
	}
}

func TestIntegrationRecoveryFailedStatusDoesNotBecomeSuccessfulRecovery(t *testing.T) {
	a := executor.IntegrationApproval{ID: strings.Repeat("b", 64), TestPin: executor.Pin{PermitID: strings.Repeat("a", 32)}, ExecutionID: strings.Repeat("c", 32), Repository: executor.RepositoryIdentity{CommonDir: "/root/repos/controlled.git", Device: 1, Inode: 2}, Request: integrationqueue.Request{Owner: "owner", Commit: strings.Repeat("d", 40)}}
	claim := executor.IntegrationClaim{Version: 1, ApprovalID: a.ID, Repository: a.Repository, ExecutionID: a.ExecutionID, Candidate: a.Request.Commit, Owner: a.Request.Owner, Generation: 1, CreatedAt: time.Now(), State: "FAILED"}
	raw, _ := json.Marshal(claim)
	in := IntegrationRecoveryInput{PermitID: a.TestPin.PermitID, ApprovalID: a.ID}
	got, e := integrationRecoveryWithApproval(context.Background(), nil, in, true, a, func(_ context.Context, action, permit, approval string) ([]byte, error) {
		if action != "integration-recovery-status" || permit != in.PermitID || approval != in.ApprovalID {
			t.Fatal("status authority pins lost")
		}
		return raw, nil
	})
	if e != nil || got.State != "FAILED" {
		t.Fatal("FAILED root evidence rejected", e)
	}
	if validIntegrationRecoveryState("FAILED", false) {
		t.Fatal("FAILED accepted as successful mutation")
	}
	for _, state := range []string{"HELD", "EFFECT_INTENT", "RELEASED", "FAILED", "UNKNOWN", "DONE", ""} {
		if validIntegrationRecoveryState(state, false) != (state == "RELEASED") {
			t.Fatal("mutation success state changed", state)
		}
	}
}
