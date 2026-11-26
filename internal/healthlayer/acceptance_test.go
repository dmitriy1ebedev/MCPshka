package healthlayer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestControlPathFaultDetectedAfterCacheExpires(t *testing.T) {
	var down atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	c := New([]Probe{{Name: "control", Read: func(ctx context.Context) (Sample, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
		response, e := http.DefaultClient.Do(req)
		if e != nil {
			return Sample{}, e
		}
		defer response.Body.Close()
		state := "ok"
		if response.StatusCode != 200 {
			state = "degraded"
		}
		return Sample{State: state}, nil
	}}})
	r, e := c.Read(context.Background())
	if e != nil || r.Items[0].State != "ok" {
		t.Fatal("initial path unavailable")
	}
	down.Store(true)
	cached, _ := c.Read(context.Background())
	if !cached.Cached || cached.Items[0].State != "ok" {
		t.Fatal("cache contract changed")
	}
	c.mu.Lock()
	c.cachedAt = time.Now().Add(-CacheTTL - time.Second)
	c.mu.Unlock()
	start := time.Now()
	fresh, e := c.Read(context.Background())
	if e != nil || fresh.Cached || fresh.Items[0].State != "degraded" || fresh.Items[0].Failures != 1 || time.Since(start) > time.Second {
		t.Fatalf("fault not detected: %+v %v", fresh, e)
	}
}
func TestSlowSourceDeadlineAndFailureCounter(t *testing.T) {
	c := New([]Probe{{Name: "slow", Read: func(ctx context.Context) (Sample, error) { <-ctx.Done(); return Sample{}, ctx.Err() }}})
	start := time.Now()
	r, e := c.Read(context.Background())
	if e != nil || r.Items[0].State != "unknown" || time.Since(start) > 3*time.Second {
		t.Fatalf("deadline ignored: %+v %v", r, e)
	}
	c.probes[0].Read = func(context.Context) (Sample, error) { return Sample{State: "degraded"}, nil }
	c.mu.Lock()
	c.cachedAt = time.Now().Add(-CacheTTL - time.Second)
	c.mu.Unlock()
	r, _ = c.Read(context.Background())
	if r.Items[0].Failures != 2 {
		t.Fatal("failed sample not counted")
	}
	c.probes[0].Read = func(context.Context) (Sample, error) { return Sample{State: "ok"}, nil }
	c.mu.Lock()
	c.cachedAt = time.Now().Add(-CacheTTL - time.Second)
	c.mu.Unlock()
	r, _ = c.Read(context.Background())
	if r.Items[0].Failures != 0 {
		t.Fatal("successful sample did not reset")
	}
}
func TestCombinedOutputAndInvalidConfiguration(t *testing.T) {
	probes := []Probe{}
	for i := 0; i < MaxProbes; i++ {
		probes = append(probes, Probe{Name: fmt.Sprint(i), Read: func(context.Context) (Sample, error) {
			data := make([]byte, 4000)
			for j := range data {
				data[j] = 'x'
			}
			raw, _ := json.Marshal(string(data))
			return Sample{State: "ok", Data: raw}, nil
		}})
	}
	r, e := New(probes).Read(context.Background())
	raw, _ := json.Marshal(r)
	if e != nil || len(raw) > MaxOutputBytes || len(r.Items) != MaxProbes {
		t.Fatalf("output bound failed: %d %v", len(raw), e)
	}
	if _, e = New(append(probes, probes[0])).Read(context.Background()); e == nil {
		t.Fatal("unbounded or duplicate sources accepted")
	}
}
func BenchmarkCachedSnapshot(b *testing.B) {
	c := New([]Probe{{Name: "one", Read: func(context.Context) (Sample, error) {
		return Sample{State: "ok", Data: json.RawMessage(`{"ready":true}`)}, nil
	}}})
	c.Read(context.Background())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := c.Read(context.Background()); e != nil {
			b.Fatal(e)
		}
	}
}
