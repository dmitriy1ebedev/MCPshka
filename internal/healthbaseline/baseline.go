// Package healthbaseline consumes existing observations without collecting data.
package healthbaseline

import (
	"math"
	"time"
)

const Window = 32

type Metric struct {
	Name       string    `json:"name"`
	Samples    int       `json:"samples"`
	Mean       float64   `json:"mean"`
	Outlier    bool      `json:"outlier"`
	ObservedAt time.Time `json:"observed_at"`
}

type Series struct {
	values [Window]float64
	count  int
	next   int
	at     time.Time
}

// Observe scores against the preceding bounded window. Duplicate timestamps
// cannot turn cached evidence into new samples.
func (s *Series) Observe(name string, value float64, at time.Time) Metric {
	m := Metric{Name: name, ObservedAt: s.at, Samples: s.count}
	for _, v := range s.values[:s.count] {
		m.Mean += v / float64(s.count)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || at.IsZero() || !at.After(s.at) {
		return m
	}
	if s.count >= 8 {
		var variance float64
		for _, v := range s.values[:s.count] {
			variance += (v - m.Mean) * (v - m.Mean) / float64(s.count)
		}
		m.Outlier = math.Abs(value-m.Mean) > math.Max(3*math.Sqrt(variance), math.Max(1, math.Abs(m.Mean)*0.1))
	}
	s.values[s.next] = value
	s.next = (s.next + 1) % Window
	if s.count < Window {
		s.count++
	}
	s.at = at
	m.Samples = s.count
	m.ObservedAt = at
	return m
}
