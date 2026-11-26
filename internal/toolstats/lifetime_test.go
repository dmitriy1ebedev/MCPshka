package toolstats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// MCP-151: totals outlive a restart, so "never used" means never, not
// "not since the last deploy".
func TestLifetimeSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool-usage.json")
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	first := New(t0)
	stop := first.Persist(path, time.Hour)
	first.Record("job_wait", "direct", t0, time.Second, false)
	first.Record("job_wait", "direct", t0, time.Second, true)
	stop()

	second := New(t0.Add(time.Hour))
	defer second.Persist(path, time.Hour)()
	second.Record("job_wait", "tool_invoke", t0.Add(time.Hour), time.Second, false)
	if got := second.LedgerSince(); !got.Equal(t0) {
		t.Fatalf("ledger restarted: since %v", got)
	}
	_, usage, _ := second.Report([]string{"job_wait", "fs_read"}, 0)
	for _, u := range usage {
		switch u.Tool {
		case "job_wait":
			if u.Calls != 1 || u.TotalCalls != 3 || u.TotalErrors != 1 {
				t.Fatalf("job_wait window %d total %d errors %d", u.Calls, u.TotalCalls, u.TotalErrors)
			}
		case "fs_read":
			if u.TotalCalls != 0 || u.Status != "NEVER_OBSERVED" {
				t.Fatalf("fs_read %+v", u)
			}
		}
	}
}

func TestDamagedLedgerStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool-usage.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	s := New(t0)
	stop := s.Persist(path, time.Hour)
	s.Record("fs_read", "direct", t0, time.Millisecond, false)
	stop()
	if !s.LedgerSince().Equal(t0) {
		t.Fatal("damaged ledger was not replaced")
	}
}
