package mcpserver

import (
	"strings"
	"testing"
)

func TestMCP250SambaStatusRegisteredReadOnlyAndPrivacyScoped(t *testing.T) {
	for _, tool := range catalogTools(t, fullCatalogClient(t)) {
		if tool.Name != "smb_share_status" {
			continue
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("Samba inspector must be read-only")
		}
		text := strings.ToLower(tool.Description)
		for _, phrase := range []string{"read-only", "no arbitrary paths", "password", "no service mutation"} {
			if !strings.Contains(text, phrase) {
				t.Fatalf("privacy contract missing %q", phrase)
			}
		}
		return
	}
	t.Fatal("native smb_share_status missing in MCPshka catalog")
}
