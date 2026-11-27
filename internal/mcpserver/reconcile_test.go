package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"mcpshka/internal/idempotency"
)

// MCP-135: a PENDING fs receipt is released only when the file proves the
// write never landed and no other call with the key is alive.
func TestPendingCASWriteReconciled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("old"))
	in := map[string]any{"path": path, "expected_sha256": hex.EncodeToString(sum[:]), "idempotency_key": "k1"}

	if !casUnchanged(in) {
		t.Fatal("file at expected SHA must prove no effect")
	}
	if casUnchanged(map[string]any{"path": path, "expected_sha256": "00"}) {
		t.Fatal("changed file must not prove no effect")
	}
	if !casUnchanged(map[string]any{"path": filepath.Join(dir, "absent")}) || casUnchanged(map[string]any{"path": path}) {
		t.Fatal("create proof must require an absent file")
	}

	r := &toolRegistry{store: idempotency.New(t.TempDir())}
	release := r.inflight.claim("contract:x")
	if !r.provenNoEffect("fs_patch", "contract:x", in) {
		t.Fatal("sole call with unchanged file must be provable")
	}
	second := r.inflight.claim("contract:x")
	if r.provenNoEffect("fs_patch", "contract:x", in) {
		t.Fatal("a concurrent call with the same key must keep the hold")
	}
	second()
	release()
	if r.provenNoEffect("project_commit", "contract:x", in) {
		t.Fatal("tools without a proof stay fail-closed")
	}

	if _, err := r.store.Begin("k1", "fs_patch", "fp"); err != nil {
		t.Fatal(err)
	}
	r.releaseInner("fs_patch", in)
	if begin, err := r.store.Begin("k1", "fs_patch", "fp"); err != nil || !begin.Fresh {
		t.Fatalf("inner PENDING receipt must be released: %+v %v", begin, err)
	}
}
