package mcpserver

import "testing"

// MCP-249: the RDC read-only Caddy snippet must be available through the
// public native MCPshka catalog, not merely a helper added to the source tree.
func TestMCP249HostConfigInspectRegisteredReadOnly(t *testing.T) {
	tools := catalogTools(t, fullCatalogClient(t))
	for _, tool := range tools {
		if tool.Name != "host_config_inspect" {
			continue
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("host_config_inspect must be read-only")
		}
		if tool.Description == "" {
			t.Fatal("host_config_inspect requires a bounded privacy contract")
		}
		return
	}
	t.Fatal("host_config_inspect missing from MCPshka full catalog")
}
