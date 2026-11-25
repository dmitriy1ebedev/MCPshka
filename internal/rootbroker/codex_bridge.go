package rootbroker

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
	"path/filepath"
)

// The Codex bridge ships inside the broker binary (BUG-20261021-7): deploys
// replace the broker but never touched /usr/local/libexec, so bridge fixes
// (null text, summary view) did not reach production. codex-bridge.py here
// is a byte copy of packaging/native/codex-bridge.py, enforced by a test.
//
//go:embed codex-bridge.py
var embeddedCodexBridge []byte

var (
	codexBridgeDir       = "/var/lib/mcpshka-executor/codex-bridge"
	installedCodexBridge = "/usr/local/libexec/mcpshka-codex-bridge.py"
)

// codexBridgePath materializes the embedded bridge in the broker's root-only
// state directory, named by its content hash, and falls back to the
// installed helper if that is impossible.
func codexBridgePath() string {
	sum := sha256.Sum256(embeddedCodexBridge)
	path := filepath.Join(codexBridgeDir, hex.EncodeToString(sum[:8])+".py")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, embeddedCodexBridge) && validateRootOwnedExecutable(path) == nil {
		return path
	}
	if err := os.MkdirAll(codexBridgeDir, 0o700); err != nil {
		return installedCodexBridge
	}
	tmp, err := os.CreateTemp(codexBridgeDir, ".bridge-*")
	if err != nil {
		return installedCodexBridge
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(embeddedCodexBridge); err != nil || tmp.Chmod(0o700) != nil || tmp.Sync() != nil || tmp.Close() != nil {
		tmp.Close()
		return installedCodexBridge
	}
	if os.Rename(tmp.Name(), path) != nil || validateRootOwnedExecutable(path) != nil {
		return installedCodexBridge
	}
	return path
}
