package mission

import (
	"os"
	"testing"
)

func TestRecordsBudgetCountsOnlyJSON(t *testing.T) {
	dir := t.TempDir()
	// Sidecars (.prompt, .report.md, .runner.lock) do not count (MCP-166).
	for i := 0; i < 300; i++ {
		f, err := os.CreateTemp(dir, "entry-*.prompt")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if _, err := Records(dir); err != nil {
		t.Fatalf("sidecars exhausted the record budget: %v", err)
	}
	for i := 0; i < 129; i++ {
		f, err := os.CreateTemp(dir, "entry-*.json")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if _, err := Records(dir); err == nil {
		t.Fatal("unbounded scan")
	}
}
