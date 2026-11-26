package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ROADMAP S5.CM: format=ts answers with declarations instead of JSON Schemas,
// and they are at least a quarter smaller (fs_find: 1888 against 2941 bytes).
func TestToolResolveTypeScriptFormat(t *testing.T) {
	c := catalogClient(t, nil)
	first := func(args map[string]any) (map[string]any, int) {
		r := catalogCall(t, c, "tool_resolve", args)
		cands, _ := r["candidates"].([]any)
		if len(cands) != 1 {
			t.Fatalf("candidates: %+v", r)
		}
		raw, _ := json.Marshal(cands[0])
		return cands[0].(map[string]any), len(raw)
	}
	full, fullSize := first(map[string]any{"goal": "fs_find"})
	ts, tsSize := first(map[string]any{"goal": "fs_find", "format": "ts"})
	decl, _ := ts["ts"].(string)
	if full["input_schema"] == nil || ts["input_schema"] != nil || ts["output_schema"] != nil ||
		!strings.HasPrefix(decl, "declare function fs_find(args: {") || !strings.Contains(decl, "pattern: string;") {
		t.Fatalf("ts candidate: %+v", ts)
	}
	if tsSize*4 > fullSize*3 {
		t.Fatalf("ts answer %d bytes saves less than a quarter of the schema answer %d", tsSize, fullSize)
	}
	if r, err := c.CallTool(t.Context(), &mcp.CallToolParams{Name: "tool_resolve", Arguments: map[string]any{"goal": "fs_find", "format": "xml"}}); err == nil && !r.IsError {
		t.Fatal("unknown format accepted")
	}
}

// ROADMAP P19: a verified failure history is shown and breaks ties, but the
// tool stays a candidate; one error changes nothing.
func TestToolResolveShowsFailureHistoryWithoutDroppingTool(t *testing.T) {
	c := catalogClient(t, nil)
	calls, failed := 1, 1
	old := toolHistory
	toolHistory = func(name string) (int, int) {
		if name == "fs_stat" {
			return calls, failed
		}
		return 0, 0
	}
	t.Cleanup(func() { toolHistory = old })
	history := func() any {
		r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": "fs_stat", "compact": true})
		cands, _ := r["candidates"].([]any)
		if len(cands) != 1 || cands[0].(map[string]any)["name"] != "fs_stat" {
			t.Fatalf("fs_stat must stay the candidate: %+v", r)
		}
		return cands[0].(map[string]any)["failure_history"]
	}
	if h := history(); h != nil {
		t.Fatalf("one error is not a history: %v", h)
	}
	calls, failed = 40, 21
	if h, _ := history().(string); !strings.Contains(h, "failed 21 of 40 calls") {
		t.Fatalf("history: %q", h)
	}
}
