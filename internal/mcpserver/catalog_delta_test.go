package mcpserver

import (
	"mcpshka/internal/authn"
	"testing"
)

func TestCapabilityDeltaFiltersBeforeIndexing(t *testing.T) {
	c := catalogClient(t, &authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"tool_resolve", "fs_find"}})
	out := catalogCall(t, c, "tool_resolve", map[string]any{"goal": "catalog", "catalog_delta": true, "limit": 32})
	delta := out["catalog"].(map[string]any)
	for _, value := range delta["changed"].([]any) {
		item := value.(map[string]any)
		if item["id"] != "tool_resolve" && item["id"] != "fs_find" {
			t.Fatal("unauthorized tool indexed", item)
		}
		if item["schema_hash"] == "" || item["tool_hash"] == "" || item["executable"] != false {
			t.Fatal(item)
		}
	}
	if delta["revision"] == "" {
		t.Fatal(out)
	}
}
