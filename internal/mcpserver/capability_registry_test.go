package mcpserver

import (
	"context"
	"encoding/json"
	"mcpshka/internal/readiness"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func catalogClient(t *testing.T, identity *authn.Identity) *mcp.ClientSession {
	t.Helper()
	return newCatalogClient(t, identity, false)
}

// fullCatalogClient lists the whole registry, hidden tools included.
func fullCatalogClient(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return newCatalogClient(t, nil, true)
}

func newCatalogClient(t *testing.T, identity *authn.Identity, full bool) *mcp.ClientSession {
	t.Helper()
	p := policy.Default()
	p.AllowedWorkdirs = []string{t.TempDir()}
	m, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	s := New(m, filesystem.New(p))
	ctx := context.Background()
	if identity != nil {
		ctx = authn.WithIdentity(ctx, *identity)
	}
	if full {
		ctx = context.WithValue(ctx, fullCatalogKey{}, true)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "catalog-test", Version: "1"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func catalogTools(t *testing.T, c *mcp.ClientSession) []*mcp.Tool {
	t.Helper()
	var tools []*mcp.Tool
	p := &mcp.ListToolsParams{}
	seen := map[string]bool{}
	for {
		page, err := c.ListTools(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" {
			return tools
		}
		// A cursor that comes back again is an endless listing, not a slow one.
		if seen[page.NextCursor] || len(seen) > 1000 {
			t.Fatalf("tools/list pagination does not terminate: cursor %q repeated after %d pages, %d tools", page.NextCursor, len(seen), len(tools))
		}
		seen[page.NextCursor] = true
		p.Cursor = page.NextCursor
	}
}

func TestCapabilityCatalogCompletenessAndBudget(t *testing.T) {
	c := fullCatalogClient(t)
	tools := catalogTools(t, c)
	boot := catalogCall(t, c, "capabilities_bootstrap", map[string]any{})
	if int(boot["registered_count"].(float64)) != len(tools) {
		t.Fatalf("catalog count does not match tools/list: %v vs %d", boot["registered_count"], len(tools))
	}
	for _, tool := range tools {
		r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": tool.Name})
		if tool.Name == "broker_install" && os.Geteuid() != 0 {
			continue
		}
		if r["code"] == "SCHEMA_OUTPUT_LIMIT" {
			continue
		}
		cs := r["candidates"].([]any)
		if len(cs) != 1 {
			t.Fatalf("registered tool missing from catalog: %s: %+v", tool.Name, r)
		}
		got := cs[0].(map[string]any)
		for _, field := range []struct {
			name string
			want any
		}{{"input_schema", tool.InputSchema}, {"output_schema", tool.OutputSchema}} {
			a, _ := json.Marshal(got[field.name])
			b, _ := json.Marshal(field.want)
			if string(a) != string(b) {
				t.Fatalf("%s %s drift: %s vs %s", tool.Name, field.name, a, b)
			}
		}
	}
	a, _ := json.Marshal(boot)
	b, _ := json.Marshal(tools)
	if len(a) > 8192 || len(a)*10 >= len(b) {
		t.Fatalf("bootstrap not compact: bootstrap=%d listing=%d", len(a), len(b))
	}
	t.Logf("bootstrap=%d bytes; full tools/list=%d bytes", len(a), len(b))
}

func TestToolResolveCompactKeepsInputSchemaAndSavesTokens(t *testing.T) {
	c := catalogClient(t, nil)
	var full, compact int
	for _, tool := range catalogTools(t, c) {
		if tool.Name == "broker_install" && os.Geteuid() != 0 {
			continue
		}
		long := catalogCall(t, c, "tool_resolve", map[string]any{"goal": tool.Name})
		short := catalogCall(t, c, "tool_resolve", map[string]any{"goal": tool.Name, "compact": true})
		if long["code"] == "SCHEMA_OUTPUT_LIMIT" {
			continue
		}
		got := short["candidates"].([]any)[0].(map[string]any)
		if _, ok := got["output_schema"]; ok || got["name"] != tool.Name {
			t.Fatalf("%s compact candidate carries output schema or wrong tool: %v", tool.Name, got["name"])
		}
		a, _ := json.Marshal(got["input_schema"])
		b, _ := json.Marshal(tool.InputSchema)
		if string(a) != string(b) {
			t.Fatalf("%s compact input schema drift", tool.Name)
		}
		for _, field := range []string{"constraints", "why", "client_exposed"} {
			if _, ok := got[field]; ok {
				t.Fatalf("%s compact keeps %s", tool.Name, field)
			}
		}
		l, _ := json.Marshal(long)
		s, _ := json.Marshal(short)
		full, compact = full+len(l), compact+len(s)
	}
	if compact*2 > full {
		t.Fatalf("compact resolve saves too little: %d vs %d bytes", compact, full)
	}
	t.Logf("tool_resolve over the catalog: full=%d compact=%d bytes", full, compact)
}

func TestCapabilityAuthorizationAliasesAndValidation(t *testing.T) {
	c := catalogClient(t, &authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"capabilities_bootstrap", "tool_resolve", "fs_find", "exec_start"}})
	b := catalogCall(t, c, "capabilities_bootstrap", map[string]any{})
	if b["authorized_count"] != float64(3) {
		t.Fatalf("reader permissions inflated: %+v", b)
	}
	for _, goal := range []string{"fs_find", "найти файл", "find files"} {
		r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": goal})
		cs := r["candidates"].([]any)
		if len(cs) == 0 || cs[0].(map[string]any)["name"] != "fs_find" {
			t.Fatalf("resolve %q: %+v", goal, r)
		}
	}
	for _, goal := range []string{"exec_start", "git_status", "unregistered_external_plugin"} {
		r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": goal})
		if len(r["candidates"].([]any)) != 0 {
			t.Fatalf("unauthorized recommendation %q: %+v", goal, r)
		}
	}
	for _, in := range []map[string]any{{"goal": ""}, {"goal": strings.Repeat("x", 257)}, {"goal": "fs_find", "expected_revision": "bad"}} {
		r, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "tool_resolve", Arguments: in})
		if err == nil && !r.IsError {
			t.Fatalf("invalid arguments accepted: %+v", in)
		}
	}
}

func TestCapabilityRevisionTracksAuthorizationAndPolicy(t *testing.T) {
	r := &toolRegistry{catalogRevision: "catalog", policyRevision: "policy"}
	ctx := func(id, role string, scopes ...string) context.Context {
		return authn.WithIdentity(context.Background(), authn.Identity{ID: id, Role: role, Scopes: scopes})
	}
	base, _, err := r.capabilityRevision(ctx("one", "operator", "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	same, _, _ := r.capabilityRevision(ctx("two", " OPERATOR ", "b", "a", "a"))
	if same != base {
		t.Fatal("identity ID or scope order changed metadata revision")
	}
	for _, c := range []context.Context{ctx("one", "reader", "a", "b"), ctx("one", "operator", "a")} {
		got, _, _ := r.capabilityRevision(c)
		if got == base {
			t.Fatal("authorization change did not invalidate revision")
		}
	}
	r.policyRevision = "new policy"
	got, _, _ := r.capabilityRevision(ctx("one", "operator", "a", "b"))
	if got == base {
		t.Fatal("policy change did not invalidate revision")
	}
}

func TestCapabilityAliasesReferenceRegisteredActions(t *testing.T) {
	c := fullCatalogClient(t)
	names := map[string]bool{}
	for _, tool := range catalogTools(t, c) {
		names[tool.Name] = true
	}
	for name := range capabilityAliases {
		if !names[name] {
			t.Fatalf("alias references unregistered tool %q", name)
		}
	}
	for _, tc := range []struct{ goal, want string }{
		{"задача проекта", "project_run"}, {"project task", "project_run"},
		{"тесты проекта", "project_test"}, {"run tests", "project_test"},
		{"состояние резервной копии", "state_backup_status"},
	} {
		r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": tc.goal})
		cs := r["candidates"].([]any)
		if len(cs) == 0 || cs[0].(map[string]any)["name"] != tc.want {
			t.Fatalf("%q: want %s, got %+v", tc.goal, tc.want, r)
		}
		if tc.want == "project_run" || tc.want == "project_test" {
			candidate := cs[0].(map[string]any)
			if candidate["effect"] != "gated_mutation" || candidate["mutation"] == nil || candidate["ready"] != readiness.Preflight || candidate["backend_healthy"] != "unknown" {
				t.Fatalf("mutator gate metadata lost: %+v", candidate)
			}
		}
	}
}

func TestCapabilityResolutionBounds(t *testing.T) {
	r := &toolRegistry{
		catalog: map[string]*mcp.Tool{"oversized": {Name: "oversized", Description: strings.Repeat("x", 40<<10), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}},
		tools:   map[string]bool{"oversized": true}, catalogRevision: "test", policyRevision: "test",
	}
	out, err := r.resolveCapabilities(context.Background(), ToolResolveInput{Goal: "oversized"})
	if err != nil || out.Code != "SCHEMA_OUTPUT_LIMIT" || len(out.Candidates) != 0 || out.Instructions == "" {
		t.Fatalf("large schema was silently truncated: %+v / %v", out, err)
	}
	for _, revision := range []string{strings.Repeat("a", 1024), strings.Repeat("A", 64)} {
		if _, err := r.resolveCapabilities(context.Background(), ToolResolveInput{Goal: "oversized", ExpectedRevision: revision}); err == nil {
			t.Fatal("invalid revision accepted")
		}
	}
}

func catalogCall(t *testing.T, c *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	r, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || r.IsError {
		text := ""
		if r != nil {
			for _, c := range r.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					text += tc.Text
				}
			}
		}
		t.Fatalf("%s: %v: %s", name, err, text)
	}
	raw, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		t.Fatal("invalid catalog output")
	}
	return out
}

func TestCapabilityBootstrapAndResolveUseRegisteredSchemas(t *testing.T) {
	c := catalogClient(t, nil)
	boot := catalogCall(t, c, "capabilities_bootstrap", map[string]any{})
	revision, _ := boot["revision"].(string)
	if len(revision) != 64 || boot["client_exposed"] != "unknown" {
		t.Fatalf("invalid bootstrap: %+v", boot)
	}
	list, err := c.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var find *mcp.Tool
	for _, tool := range list.Tools {
		if tool.Name == "fs_find" {
			find = tool
			break
		}
	}
	if find == nil {
		t.Fatal("registered fs_find missing")
	}
	resolved := catalogCall(t, c, "tool_resolve", map[string]any{"goal": "fs_find", "expected_revision": revision})
	candidates, ok := resolved["candidates"].([]any)
	if !ok || len(candidates) != 1 {
		t.Fatalf("exact tool resolution: %+v", resolved)
	}
	got := candidates[0].(map[string]any)
	a, _ := json.Marshal(got["input_schema"])
	b, _ := json.Marshal(find.InputSchema)
	if string(a) != string(b) {
		t.Fatalf("catalog schema drift: %s vs %s", a, b)
	}
	stale := catalogCall(t, c, "tool_resolve", map[string]any{"goal": "fs_find", "expected_revision": strings.Repeat("0", 64)})
	if stale["code"] != "CLIENT_SCHEMA_STALE" {
		t.Fatalf("stale client accepted: %+v", stale)
	}
}
