// Package toolstats keeps privacy-safe, process-local tool telemetry
// (MCP-065, MCP-084, MCP-087): per-tool call counts, outcome classes,
// latency buckets and percentiles, last use, compatibility-route calls and a
// small ring of recent spans. It never records arguments or results.
package toolstats

import (
	"sort"
	"sync"
	"time"
)

const (
	ringPerTool = 256
	spanRing    = 200
	// RareBelow: fewer calls than this in the window is RARE, not ACTIVE.
	RareBelow = 3
)

// Span is one call without payload: who, how long, how it ended.
type Span struct {
	Tool       string    `json:"tool"`
	Route      string    `json:"route"` // direct, tool_invoke, exec_compat
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Outcome    string    `json:"outcome"` // ok, error
}

type toolCounters struct {
	calls, errors, compat int
	buckets               [4]int // <=100ms, <=1s, <=10s, >10s
	last                  time.Time
	ring                  []int64
	next                  int
}

// Usage is the windowed view of one tool.
type Usage struct {
	Tool        string    `json:"tool"`
	Status      string    `json:"status"` // ACTIVE, RARE, NEVER_OBSERVED
	Calls       int       `json:"calls"`
	Errors      int       `json:"errors"`
	CompatCalls int       `json:"compat_calls"`
	Buckets     [4]int    `json:"latency_buckets"`
	P50MS       int64     `json:"p50_ms"`
	P95MS       int64     `json:"p95_ms"`
	P99MS       int64     `json:"p99_ms"`
	LastUsed    time.Time `json:"last_used,omitempty"`
	// Lifetime totals since LedgerSince, across restarts.
	TotalCalls    int       `json:"total_calls"`
	TotalErrors   int       `json:"total_errors"`
	TotalLastUsed time.Time `json:"total_last_used,omitempty"`
	// SuccessRate is exact (calls-errors)/calls over the lifetime ledger (the
	// window without one); absent when there were no calls. RecoveryRate is
	// the share of failures whose next call of this tool succeeded.
	SuccessRate  *float64 `json:"success_rate,omitempty"`
	Recovered    int      `json:"recovered,omitempty"`
	RecoveryRate *float64 `json:"recovery_rate,omitempty"`
}

type Stats struct {
	http                 map[string]*httpCounter
	resolutions          []resolution
	resolutionsTruncated bool
	mu                   sync.Mutex
	started              time.Time
	tools                map[string]*toolCounters
	spans                []Span
	next                 int
	path                 string
	ledger               ledger
	dirty                bool
}

func New(now time.Time) *Stats { return &Stats{started: now, tools: map[string]*toolCounters{}} }

// Default is the process-wide collector.
var Default = New(time.Now().UTC())

// Record adds one finished call.
func (s *Stats) Record(tool, route string, start time.Time, d time.Duration, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.tools[tool]
	if c == nil {
		c = &toolCounters{ring: make([]int64, 0, ringPerTool)}
		s.tools[tool] = c
	}
	s.useResolution(tool, start)
	c.calls++
	if failed {
		c.errors++
	}
	if route != "direct" {
		c.compat++
	}
	ms := d.Milliseconds()
	switch {
	case ms <= 100:
		c.buckets[0]++
	case ms <= 1000:
		c.buckets[1]++
	case ms <= 10000:
		c.buckets[2]++
	default:
		c.buckets[3]++
	}
	if end := start.Add(d); end.After(c.last) {
		c.last = end
	}
	s.recordLifetime(tool, start.Add(d).UTC(), failed)
	if len(c.ring) < ringPerTool {
		c.ring = append(c.ring, ms)
	} else {
		c.ring[c.next] = ms
		c.next = (c.next + 1) % ringPerTool
	}
	outcome := "ok"
	if failed {
		outcome = "error"
	}
	sp := Span{Tool: tool, Route: route, StartedAt: start.UTC(), DurationMS: ms, Outcome: outcome}
	if len(s.spans) < spanRing {
		s.spans = append(s.spans, sp)
	} else {
		s.spans[s.next] = sp
		s.next = (s.next + 1) % spanRing
	}
}

func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := (len(sorted)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	return sorted[i-1]
}

// Report lists every known tool (registered names never called are
// NEVER_OBSERVED) for the window since process start, newest spans last.
func (s *Stats) Report(registered []string, spans int) (time.Time, []Usage, []Span) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := map[string]bool{}
	for _, n := range registered {
		names[n] = true
	}
	for n := range s.tools {
		names[n] = true
	}
	for n := range s.ledger.Tools {
		names[n] = true
	}
	var out []Usage
	for n := range names {
		u := Usage{Tool: n, Status: "NEVER_OBSERVED"}
		if c := s.tools[n]; c != nil {
			lat := append([]int64(nil), c.ring...)
			sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
			u = Usage{Tool: n, Status: "ACTIVE", Calls: c.calls, Errors: c.errors, CompatCalls: c.compat, Buckets: c.buckets,
				P50MS: percentile(lat, 50), P95MS: percentile(lat, 95), P99MS: percentile(lat, 99), LastUsed: c.last}
			if c.calls < RareBelow {
				u.Status = "RARE"
			}
		}
		if l := s.ledger.Tools[n]; l != nil {
			u.TotalCalls, u.TotalErrors, u.TotalLastUsed = l.Calls, l.Errors, l.Last
			u.Recovered, u.RecoveryRate = l.Recovered, Rate(l.Recovered, l.Errors)
		}
		if u.SuccessRate = Rate(u.TotalCalls-u.TotalErrors, u.TotalCalls); u.SuccessRate == nil {
			u.SuccessRate = Rate(u.Calls-u.Errors, u.Calls)
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Tool < out[j].Tool
	})
	ordered := make([]Span, 0, len(s.spans))
	if len(s.spans) == spanRing {
		ordered = append(ordered, s.spans[s.next:]...)
		ordered = append(ordered, s.spans[:s.next]...)
	} else {
		ordered = append(ordered, s.spans...)
	}
	if spans >= 0 && len(ordered) > spans {
		ordered = ordered[len(ordered)-spans:]
	}
	return s.started, out, ordered
}

// Chains measures how tools are used in runs: calls no more than gap apart
// form one chain (a diagnosis, a ROADMAP→phase_gate pass, a release). Compat
// counts calls that came through tool_invoke or the exec bridge, i.e. the
// client's own tools/list lacked the tool (CLIENT_SCHEMA_STALE in practice).
type Chains struct {
	Spans     int     `json:"spans"`
	Count     int     `json:"chains"`
	MeanCalls float64 `json:"mean_calls"`
	MaxCalls  int     `json:"max_calls"`
	Compat    int     `json:"compat_calls"`
}

// ChainsOf expects spans oldest first, as Report returns them.
func ChainsOf(spans []Span, gap time.Duration) Chains {
	c := Chains{Spans: len(spans)}
	run := 0
	var last time.Time
	for _, sp := range spans {
		if sp.Route != "direct" {
			c.Compat++
		}
		if run > 0 && sp.StartedAt.Sub(last) > gap {
			c.MaxCalls, run = max(c.MaxCalls, run), 0
		}
		if run == 0 {
			c.Count++
		}
		run++
		last = sp.StartedAt.Add(time.Duration(sp.DurationMS) * time.Millisecond)
	}
	if c.MaxCalls = max(c.MaxCalls, run); c.Count > 0 {
		c.MeanCalls = float64(len(spans)*10/c.Count) / 10
	}
	return c
}
