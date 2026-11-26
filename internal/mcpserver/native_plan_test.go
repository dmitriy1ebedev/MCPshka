package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/filesystem"
)

func TestFSReadBatchThroughProtocol(t *testing.T) {
	m, _, in := yieldFixture(t)
	a, b := filepath.Join(in.Path, "a.txt"), filepath.Join(in.Path, "b.txt")
	for path, text := range map[string]string{a: "alpha", b: "beta"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(m, filesystem.New(m.Policy()))
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"fs_read"}})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "batch-read-test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	read := func(files []filesystem.Known, limit int) filesystem.ReadResult {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "fs_read", Arguments: map[string]any{"files": files, "limit": limit}})
		if err != nil || result.IsError {
			t.Fatalf("%+v %v", result, err)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var out filesystem.ReadResult
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := read([]filesystem.Known{{Path: a}, {Path: b}, {Path: filepath.Join(in.Path, "missing")}}, 0)
	if len(first.Files) != 3 || first.Files[0].Text != "alpha" || first.Files[1].Text != "beta" || first.Files[2].Error == "" || first.Trust != "UNTRUSTED" {
		t.Fatal(first)
	}
	cached := read([]filesystem.Known{{Path: a, SHA256: first.Files[0].SHA256}, {Path: b}}, 2)
	if !cached.Files[0].Unchanged || cached.Files[0].Text != "" || cached.Files[1].Text != "be" || !cached.Files[1].Truncated {
		t.Fatal(cached)
	}
	tooMany := make([]filesystem.Known, filesystem.MaxBatchFiles+1)
	for i := range tooMany {
		tooMany[i].Path = a
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "fs_read", Arguments: map[string]any{"files": tooMany}})
	if err != nil || !result.IsError {
		t.Fatal("unbounded batch accepted", result, err)
	}
}

func TestTaskPlanShowsNativeAvailabilityWithoutExternalDispatch(t *testing.T) {
	c := catalogClient(t, &authn.Identity{ID: "planner", Role: "reader", Scopes: []string{"task_plan", "fs_read"}})
	out := catalogCall(t, c, "task_plan", map[string]any{"goal": "read files", "steps": []string{"fs_read"}})
	steps := out["steps"].([]any)
	first := steps[0].(map[string]any)
	if out["native_capability_available"] != true || out["expected_external_calls"] != float64(0) || first["native_capability_available"] != true || first["native_readiness"] == nil {
		t.Fatal(out)
	}
	denied := catalogClient(t, &authn.Identity{ID: "restricted", Role: "reader", Scopes: []string{"task_plan"}})
	out = catalogCall(t, denied, "task_plan", map[string]any{"goal": "read files", "steps": []string{"fs_read"}})
	if out["native_capability_available"] != false || out["state"] != "MISSING" || out["expected_external_calls"] != float64(0) {
		t.Fatal("planner invented permission or delegated", out)
	}
}
