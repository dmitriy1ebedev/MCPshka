package jobs

import (
	"os"
	"strings"
	"testing"
)

func TestEphemeralReceiptNeverPersistsAndInvalidates(t *testing.T) {
	m, root := testManager(t)
	revision := "a"
	j, e := m.StoreEphemeralReceipt("mcpshka-knowledge-view", []string{"knowledge", "search", "request-sha256:opaque"}, root, "PUBLIC_FACT_OWNED", "knowledge", func() error {
		if revision != "a" {
			return os.ErrNotExist
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	b, e := m.Read(j.ID, "stdout", 0, 4096)
	if e != nil || !strings.Contains(b.Text, "PUBLIC_FACT_OWNED") {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(m.StateDir() + "/" + j.ID + "/stdout.log")
	if e != nil || len(raw) != 0 {
		t.Fatal("view persisted")
	}
	revision = "b"
	if _, e = m.Read(j.ID, "stdout", 0, 4096); e == nil {
		t.Fatal("stale view returned")
	}
}
