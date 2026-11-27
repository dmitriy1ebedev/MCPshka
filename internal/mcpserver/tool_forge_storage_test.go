package mcpserver

import (
	"strings"
	"testing"

	"mcpshka/internal/toolforge"
)

func TestToolForgeAuditRealStorageDescriptions(t *testing.T) {
	client := fullCatalogClient(t)
	var tools []toolforge.Tool
	var upload toolforge.Tool
	for _, tool := range catalogTools(t, client) {
		if _, old := supersededTools[tool.Name]; old {
			continue // duplicates kept callable; they overlap by definition
		}
		ro := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		item := toolforge.Tool{Name: tool.Name, Description: tool.Description, ReadOnly: ro}
		tools = append(tools, item)
		if tool.Name == "storage_upload" {
			upload = item
		}
	}
	if upload.Description == "" {
		t.Fatal("real storage catalog missing")
	}
	for _, finding := range toolforge.Audit(tools) {
		if finding.Kind == "overlap" && strings.HasPrefix(finding.Tools[0], "storage_") {
			t.Fatalf("real storage boilerplate reported as overlap: %v", finding.Tools)
		}
	}
	response := catalogCall(t, client, "tool_forge", map[string]any{"action": "audit", "idempotency_key": "real-storage-audit"})
	findings, _ := response["findings"].([]any)
	for _, value := range findings {
		finding := value.(map[string]any)
		if finding["kind"] == "overlap" {
			for _, name := range finding["tools"].([]any) {
				if strings.HasPrefix(name.(string), "storage_") {
					t.Fatalf("tool_forge audit reports storage template overlap: %v", finding)
				}
			}
		}
	}
	// Shared safety text must not hide genuinely duplicated operation descriptions.
	upload.Description += " Export encrypted receipt bundles preserving authenticated provenance checksums and destinations."
	upload.Name = "storage_export_receipts"
	tools = append(tools, upload)
	upload.Name = "storage_export_receipts_copy"
	tools = append(tools, upload)
	for _, finding := range toolforge.Audit(tools) {
		if finding.Kind == "overlap" && finding.Tools[0] == "storage_export_receipts" && finding.Tools[1] == "storage_export_receipts_copy" {
			return
		}
	}
	t.Fatal("genuine duplicate hidden with family boilerplate")
}
