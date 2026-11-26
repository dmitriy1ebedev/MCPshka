package systeminfo

import (
	"testing"
	"time"
)

// MCP-024: the disk forecast states how much it can be trusted.
func TestStorageForecastConfidence(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s := func(h float64, free uint64, complete bool) StorageSample {
		return StorageSample{At: t0.Add(time.Duration(h * float64(time.Hour))), Total: 100 << 30, Free: free, Complete: complete}
	}
	cases := []struct {
		name     string
		old, now StorageSample
		want     string
		warn     bool
	}{
		{"high", s(0, 40<<30, true), s(6, 30<<30, true), "HIGH", true},
		{"partial", s(0, 40<<30, false), s(6, 30<<30, true), "MEDIUM", true},
		{"short", s(0, 40<<30, true), s(1, 39<<30, true), "MEDIUM", false},
		{"none", s(0, 40<<30, true), s(0.1, 30<<30, true), "LOW", false},
	}
	// 1 GiB in 1 h with 30 GiB free: reserve 20 h away, burst → no warning.
	cases[2].now.Free, cases[2].old.Free = 39<<30, 40<<30
	for _, c := range cases {
		r := storageTrend(c.old, c.now)
		if r.Confidence != c.want || r.Warning != c.warn || (c.want != "HIGH" && r.ForecastReason == "") {
			t.Fatalf("%s: confidence=%s warning=%v reason=%q", c.name, r.Confidence, r.Warning, r.ForecastReason)
		}
	}
}
