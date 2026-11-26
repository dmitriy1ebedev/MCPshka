package toolstats

import (
	"context"
	"sort"
	"sync"
	"time"
)

type httpTimingKey struct{}
type httpTiming struct {
	mu     sync.Mutex
	active int
	since  time.Time
	server time.Duration
}

// BeginHTTP starts a payload-free request clock. Server time is the union of
// handler intervals: nested tool_invoke and parallel code_run reads count once.
func BeginHTTP(ctx context.Context) (context.Context, func(string)) {
	start := time.Now()
	t := &httpTiming{}
	return context.WithValue(ctx, httpTimingKey{}, t), func(route string) {
		t.mu.Lock()
		server := t.server
		if t.active > 0 {
			server += time.Since(t.since)
		}
		t.mu.Unlock()
		Default.RecordHTTP(route, time.Since(start), server)
	}
}
func BeginHTTPTool(ctx context.Context) func() {
	t, _ := ctx.Value(httpTimingKey{}).(*httpTiming)
	if t == nil {
		return func() {}
	}
	t.mu.Lock()
	if t.active == 0 {
		t.since = time.Now()
	}
	t.active++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		t.active--
		if t.active == 0 {
			t.server += time.Since(t.since)
		}
		t.mu.Unlock()
	}
}

type HTTPUsage struct {
	Route       string  `json:"route"`
	Requests    int     `json:"requests"`
	HTTPMS      float64 `json:"http_ms"`
	ServerMS    float64 `json:"server_ms"`
	HTTPP50MS   float64 `json:"http_p50_ms"`
	HTTPP95MS   float64 `json:"http_p95_ms"`
	ServerP50MS float64 `json:"server_p50_ms"`
	ServerP95MS float64 `json:"server_p95_ms"`
}
type httpSample struct{ http, server time.Duration }
type httpCounter struct {
	requests     int
	http, server time.Duration
	ring         []httpSample
	next         int
}

func (s *Stats) RecordHTTP(route string, httpTime, serverTime time.Duration) {
	if route != "openai-tunnel" && route != "funnel/claude" && route != "local" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.http == nil {
		s.http = map[string]*httpCounter{}
	}
	c := s.http[route]
	if c == nil {
		c = &httpCounter{}
		s.http[route] = c
	}
	c.requests++
	c.http += httpTime
	c.server += serverTime
	sample := httpSample{httpTime, serverTime}
	if len(c.ring) < ringPerTool {
		c.ring = append(c.ring, sample)
	} else {
		c.ring[c.next] = sample
		c.next = (c.next + 1) % ringPerTool
	}
}
func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func (s *Stats) HTTPReport() []HTTPUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []HTTPUsage{}
	for _, route := range []string{"openai-tunnel", "funnel/claude", "local"} {
		u := HTTPUsage{Route: route}
		if c := s.http[route]; c != nil {
			u.Requests = c.requests
			u.HTTPMS = milliseconds(c.http)
			u.ServerMS = milliseconds(c.server)
			h, v := []int64{}, []int64{}
			for _, p := range c.ring {
				h = append(h, int64(p.http))
				v = append(v, int64(p.server))
			}
			sort.Slice(h, func(i, j int) bool { return h[i] < h[j] })
			sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
			u.HTTPP50MS = milliseconds(time.Duration(percentile(h, 50)))
			u.HTTPP95MS = milliseconds(time.Duration(percentile(h, 95)))
			u.ServerP50MS = milliseconds(time.Duration(percentile(v, 50)))
			u.ServerP95MS = milliseconds(time.Duration(percentile(v, 95)))
		}
		out = append(out, u)
	}
	return out
}
