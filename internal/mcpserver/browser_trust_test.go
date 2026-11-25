package mcpserver

import (
	"os"
	"path/filepath"
	"testing"

	"mcpshka/internal/browser"
)

// ROADMAP P27: screenshots are UNTRUSTED.
func TestBrowserScreenshotIsUntrusted(t *testing.T) {
	old := browser.StateDir
	browser.StateDir = t.TempDir()
	t.Cleanup(func() { browser.StateDir = old })
	id := "0123456789abcdef"
	if err := os.MkdirAll(filepath.Dir(browser.ShotPath(id)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(browser.ShotPath(id), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := catalogCall(t, fullCatalogClient(t), "browser_screenshot", map[string]any{"id": id})
	if out["trust"] != "UNTRUSTED" || out["bytes"] != float64(3) {
		t.Fatalf("screenshot answer: %+v", out)
	}
}
