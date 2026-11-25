package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"

	"mcpshka/internal/idempotency"
)

// A PENDING mutation receipt means «outcome unknown»: the call may have run.
// For a few tools the outcome is provable from the resource itself once no
// other call with that key is alive in this process. Then a repeat of the
// same call releases the key instead of staying UNKNOWN forever (MCP-135:
// fs_patch after CONTEXT_DEADLINE). Everything else stays fail-closed.
var noEffectProofs = map[string]func(map[string]any) bool{
	// Both are SHA-pinned compare-and-swap writes with an atomic rename: the
	// file still at the expected SHA (or still absent for a create) proves the
	// write never landed.
	"fs_patch": casUnchanged,
	"fs_write": casUnchanged,
}

const maxProofFileBytes = 64 << 20

func casUnchanged(in map[string]any) bool {
	path, _ := in["path"].(string)
	expected, _ := in["expected_sha256"].(string)
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	if expected == "" {
		return errors.Is(err, os.ErrNotExist)
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxProofFileBytes {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == expected
}

// keyClaims counts live calls per ledger key in this process. A handler runs
// synchronously inside its call, so a count of one (the repeat itself) proves
// no earlier attempt is still executing.
type keyClaims struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *keyClaims) claim(key string) func() {
	c.mu.Lock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[key]++
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if c.n[key]--; c.n[key] <= 0 {
			delete(c.n, key)
		}
		c.mu.Unlock()
	}
}

func (c *keyClaims) sole(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[key] == 1
}

func (r *toolRegistry) provenNoEffect(tool, ledgerKey string, in map[string]any) bool {
	proof := noEffectProofs[tool]
	return proof != nil && r.inflight.sole(ledgerKey) && proof(in)
}

// releaseInner drops the handler's own PENDING receipt for the same key
// (fs_patch/fs_write keep one under the caller's key); without it the
// released outer receipt would still replay «outcome UNKNOWN».
func (r *toolRegistry) releaseInner(tool string, in map[string]any) {
	key, _ := in["idempotency_key"].(string)
	if key == "" {
		return
	}
	if st, err := r.store.Lookup(key); err == nil && st.Operation == tool && st.State == idempotency.Pending {
		_ = r.store.Discard(key, tool, st.Fingerprint)
	}
}
