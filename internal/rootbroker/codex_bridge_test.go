package rootbroker

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedCodexBridgeIsThePackagedSource(t *testing.T) {
	packaged, err := os.ReadFile(filepath.Join("..", "..", "packaging", "native", "codex-bridge.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packaged, embeddedCodexBridge) {
		t.Fatal("internal/rootbroker/codex-bridge.py differs from packaging/native/codex-bridge.py: copy it")
	}
}

func TestCodexBridgeMaterializesOnce(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned helper check needs root")
	}
	old := codexBridgeDir
	codexBridgeDir = filepath.Join(t.TempDir(), "bridge")
	t.Cleanup(func() { codexBridgeDir = old })
	p := codexBridgePath()
	if filepath.Dir(p) != codexBridgeDir {
		t.Fatal("embedded bridge not used", p)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, embeddedCodexBridge) {
		t.Fatal("content")
	}
	if err := os.WriteFile(p, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if q := codexBridgePath(); q != p {
		t.Fatal("path moved", q)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, embeddedCodexBridge) {
		t.Fatal("tampered bridge not replaced")
	}
}
