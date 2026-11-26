package controlplane

import (
	"testing"
	"time"
)

// Isolated stand: a fake primary path drops and returns; snapshots every
// 30 s (health cadence). N=3 detects a real loss within N*30 s = 90 s and
// never fires on one or two missed probes.
func TestLossDetectionN(t *testing.T) {
	var d lossDetector
	t0 := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * 30 * time.Second) }
	// blips: one and two misses between successes
	seq := []bool{true, false, true, false, false, true}
	for i, ok := range seq {
		if d.observe(ok, at(i)).Lost {
			t.Fatalf("blip at %d declared lost", i)
		}
	}
	// a real loss starting at probe 6
	var lost Loss
	for i := 6; i < 12 && !lost.Lost; i++ {
		lost = d.observe(false, at(i))
	}
	if !lost.Lost || lost.N != LossN || lost.LostAt.Sub(lost.FirstFailureAt) != time.Duration(LossN-1)*30*time.Second {
		t.Fatalf("detection: %+v", lost)
	}
	if got := d.observe(true, at(12)); got.Lost || got.ConsecutiveFailures != 0 {
		t.Fatalf("one success must clear: %+v", got)
	}
}
