package mcpserver

import (
	"path/filepath"
	"testing"

	"mcpshka/internal/hosttriage"
	"mcpshka/internal/issues"
)

func TestSentinelFilesPersistentCriticalOnce(t *testing.T) {
	store := issues.NewStore(filepath.Join(t.TempDir(), "i.json"))
	p := hosttriage.Findings([]hosttriage.TriageProblem{{Severity: "critical", Kind: "memory_thrash", Subject: "memory", Evidence: "swap 2000 pages/s", Action: &hosttriage.TriageAction{Tool: "host_diagnose"}}, {Severity: "warning", Kind: "journal", Subject: "j"}})
	seen, filed := store.FileFindings(p, nil)
	if len(filed) != 0 {
		t.Fatalf("first sighting filed: %v", filed)
	}
	seen, filed = store.FileFindings(p, seen)
	if len(filed) != 1 {
		t.Fatalf("persistent problem not filed: %v", filed)
	}
	if _, filed = store.FileFindings(p, seen); len(filed) != 0 {
		t.Fatalf("duplicate card: %v", filed)
	}
}
