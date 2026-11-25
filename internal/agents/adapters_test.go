package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MCP-098: a third provider plugs in by an adapter file; tasks keep using the
// same Command entrypoint; invalid adapters are BLOCKED, never guessed.
func TestAdapterAddsProviderWithoutCodeChange(t *testing.T) {
	old := AdapterDir
	AdapterDir = t.TempDir()
	t.Cleanup(func() { AdapterDir = old })
	os.WriteFile(filepath.Join(AdapterDir, "deepseek.json"), []byte(`{"binary":"/bin/echo","args":["--cwd","{dir}","{prompt}"]}`), 0644)
	os.WriteFile(filepath.Join(AdapterDir, "broken.json"), []byte(`{"binary":"echo","args":["x"]}`), 0644)

	cmd, err := Command(context.Background(), "deepseek", "/tmp", "сделай X", "/tmp/r.md")
	if err != nil || cmd.Path != "/bin/echo" || strings.Join(cmd.Args[1:], " ") != "--cwd /tmp сделай X" || cmd.Dir != "/tmp" {
		t.Fatalf("adapter command: %v %v", cmd, err)
	}
	if _, err := Command(context.Background(), "broken", "/tmp", "p", ""); err == nil {
		t.Fatal("invalid adapter accepted")
	}
	if _, err := Command(context.Background(), "../etc", "/tmp", "p", ""); err == nil {
		t.Fatal("path name accepted")
	}
	got := map[string]string{}
	for _, a := range Adapters() {
		got[a.Name] = a.Ready
	}
	if got["deepseek"] != "READY" || got["broken"] != "BLOCKED" || got[Codex] == "" || got[Claude] == "" {
		t.Fatalf("adapters: %v", got)
	}
}
