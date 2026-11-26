package browser

import (
	"strings"
	"testing"
)

func TestStorageImportPinnedToAllowedOrigin(t *testing.T) {
	t.Setenv("MCPSHKA_BROWSER_HTTP_ALLOWLIST", "192.168.77.10:8123")
	s := Spec{Site: "192.168.77.10:8123", Steps: []Step{{Action: "goto", URL: "http://192.168.77.10:8123/lovelace"}}, LocalStorage: map[string]string{"hassTokens": `{"refresh_token":"owner-export"}`}}
	code, err := s.StorageScript()
	if err != nil || !strings.Contains(code, `location.origin==="http://192.168.77.10:8123"`) || !strings.Contains(code, "localStorage.setItem") {
		t.Fatalf("script not pinned: %v", err)
	}
	s.Steps = nil
	if s.Validate() == nil {
		t.Fatal("import without navigation accepted")
	}
	s.Steps = []Step{{Action: "goto", URL: "http://192.168.77.11:8123/"}}
	if _, err := s.StorageScript(); err == nil {
		t.Fatal("off-origin import accepted")
	}
	s.LocalStorage["oversized"] = strings.Repeat("x", 16385)
	if _, err := s.StorageScript(); err == nil {
		t.Fatal("oversized import accepted")
	}
}
