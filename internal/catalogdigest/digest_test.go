package catalogdigest

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
)

func TestCatalogHashStableAndSensitive(t *testing.T) {
	a := &mcp.Tool{Name: "a", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}, Description: "read", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	b := &mcp.Tool{Name: "b", InputSchema: map[string]any{"type": "object"}}
	first := Hash(map[string]*mcp.Tool{"a": a, "b": b})
	for range 20 {
		if first != Hash(map[string]*mcp.Tool{"b": b, "a": a}) {
			t.Fatal("unstable catalog")
		}
	}
	a.InputSchema = map[string]any{"properties": map[string]any{"x": map[string]any{"type": "string"}}, "type": "object"}
	if first != Hash(map[string]*mcp.Tool{"a": a, "b": b}) {
		t.Fatal("map ordering changes hash")
	}
	for _, mutate := range []func(){
		func() { a.InputSchema = map[string]any{"type": "string"} },
		func() { a.OutputSchema = map[string]any{"type": "boolean"} },
		func() { a.Description = "changed" },
		func() { a.Annotations.ReadOnlyHint = false },
		func() { a.Name = "renamed" },
	} {
		before := Hash(map[string]*mcp.Tool{"a": a, "b": b})
		mutate()
		if before == Hash(map[string]*mcp.Tool{"a": a, "b": b}) {
			t.Fatal("metadata change invisible")
		}
	}
}
