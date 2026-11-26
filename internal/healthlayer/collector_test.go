package healthlayer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSingleFlightCacheAndImmutableEvidence(t *testing.T) {
	var calls atomic.Int32
	c := New([]Probe{{Name: "control", Read: func(context.Context) (Sample, error) {
		calls.Add(1)
		return Sample{State: "ok", Data: json.RawMessage(`{"healthy":true}`)}, nil
	}}})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := c.Read(context.Background()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate probes: %d", calls.Load())
	}
	r, e := c.Read(context.Background())
	if e != nil || len(r.Items) != 1 {
		t.Fatalf("missing evidence: %+v %v", r, e)
	}
	r.Items[0].Data[0] = 'X'
	r.Items[0].Name = "changed"
	again, _ := c.Read(context.Background())
	if again.Items[0].Name != "control" || !json.Valid(again.Items[0].Data) {
		t.Fatal("cached evidence aliases response")
	}
}
func TestCancellationOutputBudgetAndErrorPrivacy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := New([]Probe{{Name: "private", Read: func(context.Context) (Sample, error) { return Sample{}, errors.New("secret-error") }}})
	if _, e := c.Read(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal("ignored cancellation")
	}
	r, e := c.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "secret-error") || len(r.Items) != 1 || r.Items[0].State != "unknown" || r.Items[0].Failures != 1 {
		t.Fatalf("unsafe evidence: %s", raw)
	}
	c = New([]Probe{{Name: "oversized", Read: func(context.Context) (Sample, error) {
		return Sample{State: "ok", Data: json.RawMessage("\"" + strings.Repeat("x", 5000) + "\"")}, nil
	}}})
	r, e = c.Read(context.Background())
	if e != nil || r.Items[0].State != "unknown" || len(r.Items[0].Data) != 0 {
		t.Fatal("unbounded output")
	}
}
func TestCancelledWaiterDoesNotLaunchAnotherProbe(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	c := New([]Probe{{Name: "slow", Read: func(ctx context.Context) (Sample, error) { close(started); <-release; return Sample{State: "ok"}, nil }}})
	go func() { defer close(done); c.Read(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := c.Read(ctx); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("waiter ignores deadline")
	}
	close(release)
	<-done
}
func TestRollupIsFailClosedAndCacheStable(t *testing.T) {
	states := []string{"ok", "stale", "unknown", "degraded"}
	probes := make([]Probe, 0, len(states))
	for i := range states {
		index := i
		probes = append(probes, Probe{Name: string(rune('a' + i)), Read: func(context.Context) (Sample, error) {
			return Sample{State: states[index]}, nil
		}})
	}
	c := New(probes)
	first, err := c.Read(context.Background())
	if err != nil || first.State != "degraded" || first.Counts != (StateCounts{OK: 1, Degraded: 1, Unknown: 1, Stale: 1}) {
		t.Fatalf("mixed source states hidden: %#v %v", first, err)
	}
	for i := range states {
		states[i] = "ok"
	}
	cached, err := c.Read(context.Background())
	if err != nil || !cached.Cached || cached.State != "degraded" || cached.Counts != first.Counts {
		t.Fatalf("cached rollup changed before refresh: %#v %v", cached, err)
	}
	c.mu.Lock()
	c.cachedAt = time.Now().Add(-CacheTTL - time.Second)
	c.mu.Unlock()
	fresh, err := c.Read(context.Background())
	if err != nil || fresh.Cached || fresh.State != "ok" || fresh.Counts.OK != len(states) || fresh.Counts.Degraded != 0 || fresh.Counts.Unknown != 0 || fresh.Counts.Stale != 0 {
		t.Fatalf("healthy refresh cannot recover: %#v %v", fresh, err)
	}
}

func TestRollupUnknownNeverBecomesHealthy(t *testing.T) {
	for _, sample := range []Sample{{State: "ok", Data: json.RawMessage("{")},
		{State: "wrong"}, {State: "stale"}} {
		c := New([]Probe{{Name: "only", Read: func(context.Context) (Sample, error) { return sample, nil }}})
		result, err := c.Read(context.Background())
		if err != nil || result.State == "ok" || result.Counts.OK > 0 {
			t.Fatalf("unsafe rollup: %+v %v", result, err)
		}
	}
	state, counts := summarize(nil)
	if state != "unknown" || counts.OK != 0 {
		t.Fatal("empty source set treated as healthy")
	}
}
