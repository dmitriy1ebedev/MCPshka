package mcpserver

import (
	"encoding/json"
	"testing"

	"mcpshka/internal/brain"
)

// The brain's direct functions pass their arguments straight to the
// server tools of the same name: every parameter must exist there.
func TestBrainFunctionsMatchServerSchemas(t *testing.T) {
	schemas := map[string]map[string]any{}
	for _, tool := range catalogTools(t, fullCatalogClient(t)) {
		raw, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]any `json:"properties"`
		}
		json.Unmarshal(raw, &s)
		schemas[tool.Name] = s.Properties
	}
	for _, f := range brain.Functions {
		if f.Name == "invoke" {
			continue
		}
		props, ok := schemas[f.Name]
		if !ok {
			t.Fatalf("%s: no server tool", f.Name)
		}
		for p := range f.Parameters["properties"].(map[string]any) {
			if _, ok := props[p]; !ok {
				t.Errorf("%s: parameter %q not in the server schema", f.Name, p)
			}
		}
	}
}
