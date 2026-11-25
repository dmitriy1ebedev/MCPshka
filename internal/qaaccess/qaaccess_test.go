package qaaccess

import (
	"errors"
	"testing"
	"time"
)

type fakeExpose struct{ open map[int]bool }

func (f *fakeExpose) Open(p int) error  { f.open[p] = true; return nil }
func (f *fakeExpose) Close(p int) error { delete(f.open, p); return nil }

// MCP-089: the URL outlives its short TTL while the E2E job is alive, closes
// with JOB_FINISHED when the job ends, never exceeds the hard cap and never
// fronts a production port.
func TestQAAccessFollowsJobLifetime(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	alive := map[string]bool{"job-1": true}
	ex := &fakeExpose{open: map[int]bool{}}
	m := New(ex, func(id string) bool { return alive[id] }, func() time.Time { return now })

	if _, err := m.Open("job-1", 8123, 0); !errors.Is(err, ErrProductionPort) {
		t.Fatalf("HA port exposed: %v", err)
	}
	if _, err := m.Open("gone", 5173, 0); !errors.Is(err, ErrNoJob) {
		t.Fatalf("no live job: %v", err)
	}
	l, err := m.Open("job-1", 5173, 20*time.Minute)
	if err != nil || !ex.open[5173] {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ { // 4 × 15 min with the job alive: past the 20-min TTL
		now = now.Add(15 * time.Minute)
		if got := m.Sweep()[0]; !got.ClosedAt.IsZero() {
			t.Fatalf("closed while job alive at %v: %+v", now, got)
		}
	}
	alive["job-1"] = false
	got := m.Sweep()[0]
	if got.CloseReason != ClosedJobFinished || ex.open[5173] || got.Remaining(now) != 0 {
		t.Fatalf("not closed on job end: %+v", got)
	}

	alive["job-2"] = true
	l, _ = m.Open("job-2", 5174, 0)
	for now.Before(l.HardUntil) {
		now = now.Add(10 * time.Minute)
		m.Sweep()
	}
	for _, x := range m.Sweep() {
		if x.ID == l.ID && x.CloseReason != ClosedHardCap {
			t.Fatalf("hard cap not enforced: %+v", x)
		}
	}
}
