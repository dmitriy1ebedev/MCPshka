package workledger

import (
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

func ledger(t *testing.T, now *time.Time) Ledger {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 unavailable")
	}
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	return Ledger{DB: db, Now: func() time.Time { return *now }}
}

// MCP-063: of concurrent claimers exactly one wins; after a crash the claim
// expires, the next owner gets epoch 2 and the old owner's effect is fenced.
func TestClaimSingleWinnerAndFencedTakeover(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := ledger(t, &now)
	var wins, refused int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := l.Acquire("release-030", "owner-"+string(rune('a'+i)), time.Minute)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if errors.Is(err, ErrClaimed) {
				refused++
			} else {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 || refused != 5 {
		t.Fatalf("wins %d refused %d", wins, refused)
	}

	now = time.Unix(1_800_000_000, 0)
	old, _ := l.Acquire("crash", "first", time.Minute)
	old, err := l.Record(old, "build", "INTENT", nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute) // first crashed; claim expired
	succ, err := l.Acquire("crash", "second", time.Minute)
	if err != nil || succ.Epoch != 2 {
		t.Fatalf("takeover: %+v %v", succ, err)
	}
	if _, err := l.Record(old, "build", "CONFIRMED", nil); !errors.Is(err, ErrFenced) {
		t.Fatalf("stale owner recorded an effect: %v", err)
	}
	// MCP-062: the successor rebuilds from the ledger: build is UNKNOWN
	// (intent without confirmation) and must be reconciled, not repeated.
	r, err := l.Replay("crash")
	if err != nil || len(r.Events) != 1 || len(r.Unknown) != 1 || r.Unknown[0] != "build" || r.LastConfirmed != "" {
		t.Fatalf("replay: %+v %v", r, err)
	}
	succ, _ = l.Record(succ, "build", "CONFIRMED", nil)
	succ, _ = l.Record(succ, "deploy", "INTENT", nil)
	if _, err := l.Record(succ, "deploy", "CONFIRMED", nil); err != nil {
		t.Fatal(err)
	}
	r, _ = l.Replay("crash")
	if len(r.Events) != 4 || r.LastConfirmed != "deploy" || len(r.Unknown) != 0 {
		t.Fatalf("replay after confirm: %+v", r)
	}
	if again, err := l.Acquire("crash", "second", time.Minute); err != nil || again.Epoch != 2 {
		t.Fatalf("idempotent re-acquire: %+v %v", again, err)
	}
}
