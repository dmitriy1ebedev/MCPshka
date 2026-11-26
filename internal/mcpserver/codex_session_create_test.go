package mcpserver

import (
	"context"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/idempotency"
)

func TestCodexSessionCompatibilityRejectsCallerAuthorityAndExecutionOverrides(t *testing.T) {
	valid := ExecStartInput{Command: "mcpshka", Args: []string{"codex-session", `{"action":"status","admin_session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","creation_key":"cccccccccccccccccccccccccccccccc"}`}, IdempotencyKey: "key"}
	if _, err := codexSessionCompatRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"action":"create","owner":"forged"}`, `{"action":"/bin/sh"}`, `{"action":"status"} trailing`, `{"action":"create","thread_id":"arbitrary-thread"}`} {
		in := valid
		in.Args = []string{"codex-session", raw}
		if _, err := codexSessionCompatRequest(in); err == nil {
			t.Fatal("caller authority accepted")
		}
	}
	in := valid
	in.Env = map[string]string{"HOME": "override"}
	if _, err := codexSessionCompatRequest(in); err == nil {
		t.Fatal("environment override accepted")
	}
}

func TestCodexSessionProbePinsNaturalOwnerBoundKeys(t *testing.T) {
	want := map[string]any{"admin_session_id": "session", "creation_key": "creation", "load_key": "load"}
	r := &toolRegistry{readers: map[string]probeHandler{"codex_session_creation_status": func(_ context.Context, args map[string]any) (any, error) {
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("probe lost keys: %v", args)
		}
		return map[string]any{"state": "READY"}, nil
	}}}
	for _, name := range []string{"codex_session_create", "codex_session_load", "exec_start"} {
		input := map[string]any{"admin_session_id": "session", "creation_key": "creation", "load_key": "load"}
		if name == "exec_start" {
			input = map[string]any{"command": "mcpshka", "args": []any{"codex-session", `{"action":"load","admin_session_id":"session","creation_key":"creation","load_key":"load"}`}, "idempotency_key": "key"}
		}
		if _, err := r.evidence(context.Background(), name, input, nil, true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodexSessionCachedMutationCannotCrossAuthenticatedOwners(t *testing.T) {
	for _, name := range []string{"codex_session_create", "codex_session_load"} {
		t.Run(name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "archive-test", Version: "1"}, nil)
			r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
			r.readers["codex_session_creation_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "PREPARED"}, nil }
			effects := 0
			addTool(r, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(context.Context, *mcp.CallToolRequest, CodexSessionCreateInput) (*mcp.CallToolResult, map[string]any, error) {
				effects++
				return nil, map[string]any{"state": "VERIFIED"}, nil
			})
			for _, owner := range []string{"owner-a", "owner-a", "owner-b"} {
				ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: owner, Role: "operator", Scopes: []string{name}})
				st, ct := mcp.NewInMemoryTransports()
				ss, err := server.Connect(ctx, st, nil)
				if err != nil {
					t.Fatal(err)
				}
				cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
				if err != nil {
					ss.Close()
					t.Fatal(err)
				}
				result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"admin_session_id": "same-session", "creation_key": "cccccccccccccccccccccccccccccccc", "idempotency_key": "same-key"}})
				cs.Close()
				ss.Close()
				if err != nil {
					t.Fatal(err)
				}
				if result.IsError != (owner == "owner-b") {
					t.Fatalf("owner %s got error=%v", owner, result.IsError)
				}
			}
			if effects != 1 {
				t.Fatalf("cross-owner or repeated archive effect: %d", effects)
			}
		})
	}
}

func TestCodexSessionCompatibilityChecksTargetScopeAndOwnerBeforeCache(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "archive-compat-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	r.readers["codex_session_creation_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "PREPARED"}, nil }
	effects := 0
	addTool(r, &mcp.Tool{Name: "exec_start", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(context.Context, *mcp.CallToolRequest, ExecStartInput) (*mcp.CallToolResult, map[string]any, error) {
		effects++
		return nil, map[string]any{"state": "done"}, nil
	})
	for _, tc := range []struct {
		owner    string
		scopes   []string
		rejected bool
	}{
		{"owner-a", []string{"*"}, false}, {"owner-a", []string{"*"}, false}, {"owner-b", []string{"*"}, true}, {"owner-a", []string{"exec_start"}, true},
	} {
		ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: tc.owner, Role: "operator", Scopes: tc.scopes})
		st, ct := mcp.NewInMemoryTransports()
		ss, err := server.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			ss.Close()
			t.Fatal(err)
		}
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "exec_start", Arguments: map[string]any{"command": "mcpshka", "args": []string{"codex-session", `{"action":"create","admin_session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","creation_key":"cccccccccccccccccccccccccccccccc"}`}, "idempotency_key": "same-key"}})
		cs.Close()
		ss.Close()
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != tc.rejected {
			t.Fatalf("owner %s scopes%v got error=%v", tc.owner, tc.scopes, result.IsError)
		}
	}
	if effects != 1 {
		t.Fatalf("compatibility replay/owner/scope bypass: %d effects", effects)
	}
}
