package durabletimer

import (
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

// MCP-008 (BUG-114): an 8-hour stall with a 30-minute timer yields one
// coalesced event, not 15; concurrent ticks never double-fire; the watchdog
// sees the stall.
func TestTimerCoalescesOutageAndDedupsConcurrentTicks(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 unavailable")
	}
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := Store{DB: db}
	start := time.Date(2026, 10, 26, 21, 35, 0, 0, time.UTC)
	if err := s.Add(Timer{ID: "control-check", Start: start, Period: 30 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Tick(start.Add(time.Minute)); len(f) != 1 || f[0].Slot != 0 {
		t.Fatalf("first: %+v", f)
	}
	if st, _ := s.Watchdog(start.Add(8*time.Hour), time.Hour); st != "STALE" {
		t.Fatalf("watchdog: %s", st)
	}
	late := start.Add(7*time.Hour + 45*time.Minute)
	var mu sync.Mutex
	var all []Fire
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := s.Tick(late)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			all = append(all, f...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(all) != 1 || all[0].Slot != 15 || all[0].Missed != 14 {
		t.Fatalf("after outage: %+v", all)
	}
	if st, _ := s.Watchdog(late.Add(time.Minute), time.Hour); st != "OK" {
		t.Fatalf("watchdog after tick: %s", st)
	}
}
