package systeminfo

import (
	"testing"
	"time"
)

func TestStorageForecastWarnsBeforeReserve(t *testing.T) {
	now := time.Now()
	old := StorageSample{At: now.Add(-time.Hour), Total: 60 << 30, Free: 15 << 30, Complete: true, Sizes: map[string]int64{"/var/lib/docker": 10 << 30}}
	next := StorageSample{At: now, Total: 60 << 30, Free: 14 << 30, Complete: true, Sizes: map[string]int64{"/var/lib/docker": 11 << 30}}
	r := storageTrend(old, next)
	if r.BytesPerHour != 1<<30 || r.HoursToReserve != 8 || !r.Warning || len(r.Growing) != 1 {
		t.Fatalf("%+v", r)
	}
	next.Free = 16 << 30
	r = storageTrend(old, next)
	if r.HoursToReserve != 0 || r.BytesPerHour != 0 {
		t.Fatalf("cleanup predicts growth: %+v", r)
	}
}
func TestStorageTrendDoesNotComparePartialDirectories(t *testing.T) {
	now := time.Now()
	a := StorageSample{At: now.Add(-time.Hour), Total: 60 << 30, Free: 15 << 30, Sizes: map[string]int64{"/root": 1}}
	b := StorageSample{At: now, Total: 60 << 30, Free: 14 << 30, Complete: true, Sizes: map[string]int64{"/root": 100}}
	r := storageTrend(a, b)
	if len(r.Growing) != 0 || r.BytesPerHour != 1<<30 {
		t.Fatalf("%+v", r)
	}
}
