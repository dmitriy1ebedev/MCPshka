package toolcontract

import (
	"strings"
	"testing"
)

func FuzzMutationRetryKey(f *testing.F) {
	for _, seed := range []string{"request-1", "", " \t", "task\x00replacement", strings.Repeat("x", 257)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if len(key) > 8192 {
			return
		}
		c := contract("state_status", "bounded_expiry", "idempotency_key")
		got, err := RetryKey(c, map[string]any{"idempotency_key": key})
		invalid := strings.TrimSpace(key) == "" || len(key) > 256 || strings.ContainsRune(key, 0)
		if invalid && err == nil {
			t.Fatal("unsafe replay key accepted")
		}
		if !invalid && (err != nil || got != key) {
			t.Fatal("valid key lost identity")
		}
	})
}
