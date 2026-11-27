package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/projectctx"
	"mcpshka/internal/projectdrift"
	"mcpshka/internal/statestore"
)

func TestProjectContextBranchAndProductionDriftThroughProtocol(t *testing.T) {
	withIssueStore(t)
	m, _, in := yieldFixture(t)
	old := projectctx.Drift
	t.Cleanup(func() { projectctx.Drift = old })
	projectctx.Drift = func(_ *statestore.DB, root, common, controller string, now time.Time) []projectdrift.Flag {
		if root != in.Path || common == "" {
			t.Fatal("scope identity missing")
		}
		return projectdrift.Evaluate([]byte("### Релизы\n| Версия | Дата | Статус | Что |\n| 0.12.8 | 2026-11-13 | в проде | old |\n"), "0.12.9", "0.12.8", controller, "0.13.0", now)
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "drift-test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerProjectContext(r, m)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"project_context"}})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := r.server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(args map[string]any) ProjectContext {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_context", Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%+v %v", result, err)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var out ProjectContext
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := call(map[string]any{"path": in.Path, "expected_branch": "foreign/branch"})
	if out.BranchCheck.State != "MISMATCH" || !slices.Contains(out.Warnings, "branch_mismatch") || len(out.Drift) != 3 || out.Lease.LeaseID != in.LeaseID || !out.Lease.Held {
		t.Fatalf("%+v", out)
	}
	for _, drift := range out.Drift {
		if drift.State != "DRIFT" || drift.Mismatch == nil || !*drift.Mismatch {
			t.Fatal(drift)
		}
	}
	if !strings.Contains(strings.Join(out.NextActions, " "), "git_worktree op=list") {
		t.Fatal(out.NextActions)
	}
	if got := call(map[string]any{"path": in.Path}); got.BranchCheck.State != "UNVERIFIED" {
		t.Fatal("branch ownership invented", got.BranchCheck)
	}
}
