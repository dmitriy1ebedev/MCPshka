package mcpserver

import (
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/catalogdigest"
	"testing"
)

// ROADMAP S5.CM baseline: what a client pays to learn the catalog. The whole
// tools/list against capabilities_bootstrap plus one resolved operation as a
// TypeScript declaration. The numbers are logged for the release notes; the
// compact path must stay under a fifth of the full listing.
func TestDiscoveryBudgetBootstrapVersusToolsList(t *testing.T) {
	c := catalogClient(t, nil)
	size := func(v any) int {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return len(raw)
	}
	tools := catalogTools(t, c)
	list := size(tools)
	boot := size(catalogCall(t, c, "capabilities_bootstrap", map[string]any{}))
	schema := size(catalogCall(t, c, "tool_resolve", map[string]any{"goal": "project_test"}))
	compact := size(catalogCall(t, c, "tool_resolve", map[string]any{"goal": "project_test", "compact": true}))
	ts := size(catalogCall(t, c, "tool_resolve", map[string]any{"goal": "project_test", "compact": true, "format": "ts"}))
	t.Logf("tools/list: %d tools, %d bytes (~%d tokens); bootstrap %d; resolve one tool: full %d, compact %d, compact+ts %d bytes", len(tools), list, list/4, boot, schema, compact, ts)
	if (boot+ts)*5 > list {
		t.Fatalf("bootstrap+resolve(ts) = %d bytes is over a fifth of tools/list (%d)", boot+ts, list)
	}
	if ts >= compact {
		t.Fatalf("ts answer (%d) is not smaller than the compact schema answer (%d)", ts, compact)
	}
}

// The full internal catalog must stay below 2 MiB of JSON schemas: this bounds
// connector transfer/parsing cost even for clients bypassing compact discovery.
func TestToolsListSchemaByteBudgetAndDigest(t *testing.T) {
	c := fullCatalogClient(t)
	tools := catalogTools(t, c)
	total := 0
	catalog := map[string]*mcp.Tool{}
	for _, tool := range tools {
		catalog[tool.Name] = tool
		for _, schema := range []any{tool.InputSchema, tool.OutputSchema} {
			if schema == nil {
				continue
			}
			raw, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			total += len(raw)
		}
	}
	const maxSchemaBytes = 2 << 20
	t.Logf("tools/list schemas: %d bytes, ceiling %d", total, maxSchemaBytes)
	if total > maxSchemaBytes {
		t.Fatalf("schema budget exceeded: %d > %d; shrink schemas or justify a new budget", total, maxSchemaBytes)
	}
	boot := catalogCall(t, c, "capabilities_bootstrap", map[string]any{})
	status := catalogCall(t, c, "client_catalog_status", map[string]any{})
	want := catalogdigest.Hash(catalog)
	if boot["catalog_sha256"] != want || status["catalog_sha256"] != want {
		t.Fatalf("catalog digest mismatch: %v / %v; want %s", boot["catalog_sha256"], status["catalog_sha256"], want)
	}
}
