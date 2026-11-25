package healthbaseline

import (
	"math"
	"testing"
	"time"
)

func TestWindowAndInvalidObservations(t *testing.T) {
	var s Series
	now := time.Now()
	for i := 0; i < Window+5; i++ {
		s.Observe("metric", 10, now.Add(time.Duration(i)*time.Second))
	}
	m := s.Observe("metric", 100, now.Add(time.Hour))
	if !m.Outlier || m.Samples != Window || m.Mean != 10 {
		t.Fatalf("%+v", m)
	}
	for _, at := range []time.Time{now, now.Add(time.Hour)} {
		if m := s.Observe("metric", 100, at); m.Outlier || m.Samples != Window {
			t.Fatalf("%+v", m)
		}
	}
	s.Observe("metric", math.NaN(), now.Add(2*time.Hour))
	if !s.at.Equal(now.Add(time.Hour)) {
		t.Fatal("invalid sample advanced the window")
	}
}
