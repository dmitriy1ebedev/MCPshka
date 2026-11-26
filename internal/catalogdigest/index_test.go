package catalogdigest

import (
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
	"time"
)

func TestCapabilityDeltaBoundedAndSchemaSensitive(t *testing.T) {
	now := time.Now()
	catalog := map[string]*mcp.Tool{"a": {Name: "a", InputSchema: map[string]any{"type": "object"}}, "b": {Name: "b"}}
	first, err := CatalogDelta(catalog, map[string]string{"a": "find search"}, nil, 0, 1, now, "")
	if err != nil || !first.Partial || len(first.Changed) != 1 || first.Changed[0].Executable || first.Changed[0].Trust != "UNTRUSTED" {
		t.Fatal(first, err)
	}
	known := map[string]string{"a": first.Changed[0].ToolHash, "gone": strings.Repeat("a", 64)}
	next, err := CatalogDelta(catalog, nil, known, 0, 32, now, "")
	if err != nil || next.Partial || len(next.Changed) != 1 || len(next.Removed) != 1 || next.Removed[0] != "gone" {
		t.Fatal(next, err)
	}
	if _, err := CatalogDelta(catalog, nil, nil, 1, 1, now, "stale"); err == nil {
		t.Fatal("stale cursor admitted")
	}
	old := Schema(catalog["a"])
	catalog["a"].OutputSchema = map[string]any{"type": "string"}
	if Schema(catalog["a"]) == old {
		t.Fatal("stale schema hash")
	}
	next, err = CatalogDelta(catalog, nil, nil, 0, 32, now, "")
	raw, _ := json.Marshal(next)
	if err != nil || len(raw) > 32768 {
		t.Fatal(len(raw), err)
	}
	if _, err := CatalogDelta(catalog, nil, nil, -1, 1, now, ""); err == nil {
		t.Fatal("invalid cursor")
	}
	if _, err := CatalogDelta(catalog, nil, nil, 0, 33, now, ""); err == nil {
		t.Fatal("invalid limit")
	}
}
