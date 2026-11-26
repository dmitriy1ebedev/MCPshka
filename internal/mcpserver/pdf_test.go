package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestPDFThroughMCPAndDiscovery(t *testing.T) {
	for _, path := range []string{"/usr/bin/pdftotext", "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"} {
		if _, err := os.Stat(path); err != nil {
			t.Skip("PDF dependency absent:", path)
		}
	}
	dir := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{dir}}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(time.Second)
	client := parityClient(t, New(manager, filesystem.New(p)), "pdf-owner")
	path := filepath.Join(dir, "nested", "report.pdf")
	args := map[string]any{"path": path, "title": "Отчёт", "markdown": "# Кириллица\n- Проверка\n", "idempotency_key": "pdf-create"}
	via := parityCall(t, client, "tool_invoke", map[string]any{"name": "pdf_write", "arguments": args})
	first := via["output"].(map[string]any)
	again := parityCall(t, client, "pdf_write", args)
	if first["new_sha256"] != again["new_sha256"] || again["created"] != true {
		t.Fatal("PDF retry did not replay its receipt", first, again)
	}
	read := parityCall(t, client, "tool_invoke", map[string]any{"name": "pdf_read", "arguments": map[string]any{"path": path}})
	if text := read["output"].(map[string]any)["text"].(string); !strings.Contains(text, "Кириллица") || !strings.Contains(text, "Проверка") {
		t.Fatal("PDF text lost through MCP", text)
	}
	parityDenied(t, client, "pdf_write", map[string]any{"path": path, "text": "replace", "idempotency_key": "pdf-overwrite"})
	parityDenied(t, client, "pdf_write", map[string]any{"path": filepath.Join(dir, "no-key.pdf"), "text": "missing key"})
	resolved := parityCall(t, client, "tool_resolve", map[string]any{"goal": "pdf_write"})
	if resolved["candidates"].([]any)[0].(map[string]any)["name"] != "pdf_write" {
		t.Fatal("PDF tool was not discoverable", resolved)
	}
}
