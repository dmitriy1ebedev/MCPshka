package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/idempotency"
)

func TestArchiveCachedMutationCannotCrossAuthenticatedOwners(t *testing.T) {
	for _, name := range []string{"admin_archive_prepare", "admin_archive_execute", "admin_archive_reconcile"} {
		t.Run(name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "archive-test", Version: "1"}, nil)
			r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
			r.readers["admin_archive_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "PREPARED"}, nil }
			effects := 0
			addTool(r, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(context.Context, *mcp.CallToolRequest, AdminArchiveInput) (*mcp.CallToolResult, map[string]any, error) {
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
				result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"session_id": "same-session", "scope": "bootstrap-110", "idempotency_key": "same-key"}})
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

func TestArchiveCompatibilityChecksTargetScopeAndOwnerBeforeCache(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "archive-compat-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	r.readers["admin_archive_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "PREPARED"}, nil }
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
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "exec_start", Arguments: map[string]any{"command": "mcpshka", "args": []string{"admin-archive", `{"action":"execute","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"bootstrap-110"}`}, "idempotency_key": "same-key"}})
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

func TestArchiveCompatibilityRejectsInputAuthorityAndOverrides(t *testing.T) {
	valid := ExecStartInput{Command: "mcpshka", Args: []string{"admin-archive", `{"action":"status","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"bootstrap-110"}`}, IdempotencyKey: "key"}
	if _, err := archiveCompatRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"action":"status","owner":"forged"}`, `{"action":"/bin/sh"}`, `{"action":"status"} trailing`} {
		in := valid
		in.Args = []string{"admin-archive", raw}
		if _, err := archiveCompatRequest(in); err == nil {
			t.Fatal("untrusted compatibility input accepted")
		}
	}
	in := valid
	in.Env = map[string]string{"ANY": "override"}
	if _, err := archiveCompatRequest(in); err == nil {
		t.Fatal("execution override accepted")
	}
}
