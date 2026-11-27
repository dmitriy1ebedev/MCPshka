package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/idempotency"
)

func TestAdminSessionRequiresAuthenticatedOwner(t *testing.T) {
	if _, err := adminOwner(context.Background()); err == nil {
		t.Fatal("anonymous context inherited authority")
	}
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "local-stdio"})
	if _, err := adminOwner(ctx); err == nil {
		t.Fatal("stdio inherited administrative authority")
	}
	ctx = authn.WithIdentity(context.Background(), authn.Identity{ID: "owner-a", Role: "operator"})
	if owner, err := adminOwner(ctx); err != nil || owner != "owner-a" {
		t.Fatalf("authenticated owner lost: %q %v", owner, err)
	}
}

func TestAdminSessionProbesUseActualSessionAndDeployment(t *testing.T) {
	for _, tc := range []struct {
		name, probe, field, want string
		before                   bool
		in, out                  map[string]any
	}{
		{"admin_session_begin", "admin_session_status", "session_id", "requested", true, map[string]any{"session_id": "requested"}, nil},
		{"admin_session_begin", "admin_session_status", "session_id", "existing", false, map[string]any{"session_id": "requested"}, map[string]any{"session_id": "existing"}},
		{"admin_session_end", "admin_session_status", "session_id", "requested", false, map[string]any{"session_id": "requested"}, nil},
		{"admin_session_retire_deployment", "project_deploy_status", "deployment_id", "original", true, map[string]any{"deployment_id": "original"}, nil},
		{"admin_session_retire_deployment", "project_deploy_status", "deployment_id", "original", false, map[string]any{"deployment_id": "original"}, nil},
		{"admin_archive_prepare", "admin_archive_status", "session_id", "original", true, map[string]any{"session_id": "original", "scope": "bootstrap-110"}, nil},
		{"admin_archive_execute", "admin_archive_status", "scope", "bootstrap-110", true, map[string]any{"session_id": "original", "scope": "bootstrap-110"}, nil},
		{"admin_archive_reconcile", "admin_archive_status", "session_id", "original", false, map[string]any{"session_id": "original", "scope": "bootstrap-110"}, nil},
	} {
		t.Run(tc.name+tc.want, func(t *testing.T) {
			called := false
			r := &toolRegistry{readers: map[string]probeHandler{tc.probe: func(_ context.Context, args map[string]any) (any, error) {
				called = true
				if args[tc.field] != tc.want {
					t.Fatalf("wrong observation: %+v", args)
				}
				return map[string]any{"state": "observed"}, nil
			}}}
			if _, err := r.evidence(context.Background(), tc.name, tc.in, tc.out, tc.before); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("probe not called")
			}
		})
	}
}

func TestAdminSessionMutationReceiptCannotCrossOwners(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "admin-test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	r.readers["admin_session_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "active"}, nil }
	effects := 0
	addTool(r, &mcp.Tool{Name: "admin_session_end", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(context.Context, *mcp.CallToolRequest, AdminSessionEndInput) (*mcp.CallToolResult, map[string]any, error) {
		effects++
		return nil, map[string]any{"state": "closed"}, nil
	})
	for _, owner := range []string{"owner-a", "owner-a", "owner-b"} {
		ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: owner, Role: "operator", Scopes: []string{"admin_session_end"}})
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
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "admin_session_end", Arguments: map[string]any{"session_id": "same-session", "action": "close", "idempotency_key": "same-key"}})
		cs.Close()
		ss.Close()
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != (owner == "owner-b") {
			content, _ := json.Marshal(result.Content)
			t.Fatalf("owner %s got error=%v content=%s", owner, result.IsError, content)
		}
	}
	if effects != 1 {
		t.Fatalf("cross-owner or repeat effect: %d", effects)
	}
}
