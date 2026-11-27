package mcpserver

import (
	"testing"

	"mcpshka/internal/toolcontract"
)

// ChatGPT blocks an unannotated write as destructive and open-world before
// MCPshka sees it; the development flow must carry explicit hints.
func TestDevelopmentFlowHintsReachClients(t *testing.T) {
	tools := catalogTools(t, fullCatalogClient(t))
	by := map[string]bool{}
	for _, tool := range tools {
		a := tool.Annotations
		if a == nil || a.ReadOnlyHint {
			continue
		}
		by[tool.Name] = true
		if a.OpenWorldHint == nil {
			t.Errorf("%s: openWorldHint unset (MCP default true)", tool.Name)
		}
		if toolcontract.Additive(tool.Name) && (a.DestructiveHint == nil || *a.DestructiveHint) {
			t.Errorf("%s: additive tool shown as destructive", tool.Name)
		}
	}
	for _, name := range toolcontract.AdditiveTools() {
		if name != "job_wait" && !by[name] && !hiddenTool(name) {
			t.Errorf("additive %s is not a registered write tool", name)
		}
	}
}
