package healthlayer

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Conditional on callers polling at least once per second; no monitor is started.
func TestControlPathDetectionBudget(t *testing.T) {
	var down atomic.Bool
	c := New([]Probe{{Name: "control_path", Read: func(context.Context) (Sample, error) {
		state := "ok"
		if down.Load() {
			state = "degraded"
		}
		return Sample{State: state}, nil
	}}})
	if _, err := c.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	down.Store(true)
	deadline := start.Add(CacheTTL + CollectionTimeout + time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		report, err := c.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if report.Items[0].State != "ok" {
			t.Logf("control-path fixture: N=%s detected=%s output=%d", CacheTTL+CollectionTimeout+time.Second, time.Since(start), len(report.Items))
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("control loss detection budget exceeded")
		}
		<-ticker.C
	}
}

func TestHealthLoadBudget(t *testing.T) {
	var probes atomic.Int32
	c := New([]Probe{{Name: "fixed", Read: func(context.Context) (Sample, error) {
		probes.Add(1)
		return Sample{State: "ok", Data: json.RawMessage(`{"authority":"fixture"}`)}, nil
	}}})
	report, err := c.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	var before, after runtime.MemStats
	var cpuBefore, cpuAfter syscall.Rusage
	runtime.ReadMemStats(&before)
	syscall.Getrusage(syscall.RUSAGE_SELF, &cpuBefore)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				r, e := c.Read(context.Background())
				if e != nil || !r.Cached {
					t.Error("concurrent cache missed", e)
				}
			}
		}()
	}
	wg.Wait()
	runtime.ReadMemStats(&after)
	syscall.Getrusage(syscall.RUSAGE_SELF, &cpuAfter)
	raw, _ := json.Marshal(report)
	files, _ := os.ReadDir(scratch)
	cpu := func(r syscall.Rusage) int64 {
		return r.Utime.Sec*1000000 + r.Utime.Usec + r.Stime.Sec*1000000 + r.Stime.Usec
	}
	cpuUS := cpu(cpuAfter) - cpu(cpuBefore)
	allocated := after.TotalAlloc - before.TotalAlloc
	if probes.Load() != 1 || allocated > 32<<20 || len(raw) > MaxOutputBytes || len(files) != 0 || cpuUS > 2000000 {
		t.Fatalf("health budget: probes=%d alloc=%d output=%d files=%d cpu_us=%d", probes.Load(), allocated, len(raw), len(files), cpuUS)
	}
	t.Logf("1024 reads: wall=%s cpu_us=%d alloc_bytes=%d maxrss_kib=%d output_bytes=%d probe_calls=%d disk_files=%d cache_ttl=%s", time.Since(start), cpuUS, allocated, cpuAfter.Maxrss, len(raw), probes.Load(), len(files), CacheTTL)
}
