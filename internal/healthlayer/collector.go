// Package healthlayer aggregates fixed context-aware read-only probes.
// It performs no background work and never treats missing observations as healthy.
package healthlayer

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/healthbaseline"
	"sync"
	"time"
)

const (
	CacheTTL          = 15 * time.Second
	CollectionTimeout = 16 * time.Second
	ProbeTimeout      = 2 * time.Second
	MaxOutputBytes    = 16384
	MaxProbeBytes     = 4096
	MaxProbes         = 15
)

type Action struct {
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args"`
	Approval string          `json:"approval"`
}
type Freshness struct {
	ObservedAt time.Time `json:"observed_at"`
	AgeMS      int64     `json:"age_ms"`
	MaxAgeMS   int64     `json:"max_age_ms"`
}
type Sample struct {
	Action     *Action         `json:"-"`
	ObservedAt time.Time       `json:"-"`
	MaxAge     time.Duration   `json:"-"`
	State      string          `json:"state"`
	Data       json.RawMessage `json:"data,omitempty"`
}
type Probe struct {
	Name           string
	NextSafeAction *Action
	Read           func(context.Context) (Sample, error)
	// Timeout overrides ProbeTimeout for a source that is slow by nature.
	Timeout time.Duration
}
type Item struct {
	Name string `json:"name"`
	Sample
	CapturedAt     time.Time `json:"captured_at"`
	Failures       int       `json:"consecutive_failures"`
	Freshness      Freshness `json:"freshness"`
	NextSafeAction *Action   `json:"next_safe_action,omitempty"`
}
type StateCounts struct {
	OK       int `json:"ok"`
	Degraded int `json:"degraded"`
	Unknown  int `json:"unknown"`
	Stale    int `json:"stale"`
}

type Report struct {
	Baseline             []healthbaseline.Metric `json:"baseline,omitempty"`
	Trust                string                  `json:"trust"`
	State                string                  `json:"state"`
	Counts               StateCounts             `json:"counts"`
	CapturedAt           time.Time               `json:"captured_at"`
	Cached               bool                    `json:"cached"`
	AgeMS                int64                   `json:"age_ms"`
	DurationMS           int64                   `json:"duration_ms"`
	CacheTTLMS           int64                   `json:"cache_ttl_ms"`
	CollectionDeadlineMS int64                   `json:"collection_deadline_ms"`
	Items                []Item                  `json:"items"`
}
type Collector struct {
	series   map[string]*healthbaseline.Series
	probes   []Probe
	gate     chan struct{}
	mu       sync.Mutex
	last     Report
	cachedAt time.Time
	failures map[string]int
	invalid  bool
}

func New(p []Probe) *Collector {
	c := &Collector{series: map[string]*healthbaseline.Series{}, probes: append([]Probe(nil), p...), gate: make(chan struct{}, 1), failures: map[string]int{}}
	c.gate <- struct{}{}
	names := map[string]bool{}
	c.invalid = len(p) == 0 || len(p) > MaxProbes
	for _, x := range p {
		if x.Name == "" || len(x.Name) > 64 || x.Read == nil || names[x.Name] {
			c.invalid = true
		}
		names[x.Name] = true
	}
	return c
}

// summarize is fail-closed: an unobserved or malformed source cannot produce OK.
// The state describes the aggregate; counts preserve the distinct failure modes.
func summarize(items []Item) (string, StateCounts) {
	state := "ok"
	counts := StateCounts{}
	if len(items) == 0 {
		return "unknown", counts
	}
	for _, item := range items {
		switch item.State {
		case "ok":
			counts.OK++
		case "stale":
			counts.Stale++
			if state == "ok" {
				state = "stale"
			}
		case "unknown":
			counts.Unknown++
			if state != "degraded" {
				state = "unknown"
			}
		case "degraded":
			counts.Degraded++
			state = "degraded"
		default:
			counts.Unknown++
			if state != "degraded" {
				state = "unknown"
			}
		}
	}
	return state, counts
}

func copyReport(r Report) Report {
	r.Baseline = append([]healthbaseline.Metric(nil), r.Baseline...)
	r.Items = append([]Item(nil), r.Items...)
	for i := range r.Items {
		r.Items[i].Data = append(json.RawMessage(nil), r.Items[i].Data...)
		if a := r.Items[i].NextSafeAction; a != nil {
			clone := *a
			clone.Args = append(json.RawMessage(nil), a.Args...)
			r.Items[i].NextSafeAction = &clone
		}
	}
	return r
}
func (c *Collector) cached() (Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedAt.IsZero() || time.Since(c.cachedAt) >= CacheTTL {
		return Report{}, false
	}
	r := copyReport(c.last)
	r.Cached = true
	r.AgeMS = time.Since(r.CapturedAt).Milliseconds()
	c.refreshFreshness(&r)
	r.State, r.Counts = summarize(r.Items)
	return r, true
}
func boundedAction(a *Action) *Action {
	raw, err := json.Marshal(a)
	var args map[string]any
	if a == nil || err != nil || len(raw) > 512 || a.Tool == "" || json.Unmarshal(a.Args, &args) != nil || args == nil {
		return &Action{Tool: "health_snapshot", Args: json.RawMessage(`{}`), Approval: "none"}
	}
	clone := *a
	clone.Args = append(json.RawMessage(nil), a.Args...)
	return &clone
}

func (c *Collector) refreshFreshness(r *Report) {
	now := time.Now().UTC()
	for i := range r.Items {
		item := &r.Items[i]
		item.Freshness.AgeMS = now.Sub(item.Freshness.ObservedAt).Milliseconds()
		if item.Freshness.ObservedAt.After(now.Add(time.Second)) || item.Freshness.AgeMS > item.Freshness.MaxAgeMS {
			item.State = "stale"
			if item.Failures == 0 {
				item.Failures = 1
			}
			if item.NextSafeAction == nil {
				item.NextSafeAction = boundedAction(c.probes[i].NextSafeAction)
			}
		}
	}
}

func (c *Collector) Read(ctx context.Context) (Report, error) {
	if e := ctx.Err(); e != nil {
		return Report{}, e
	}
	if c.invalid {
		return Report{}, errors.New("invalid fixed health probe configuration")
	}
	if r, ok := c.cached(); ok {
		return r, nil
	}
	select {
	case <-ctx.Done():
		return Report{}, ctx.Err()
	case <-c.gate:
	}
	defer func() { c.gate <- struct{}{} }()
	if e := ctx.Err(); e != nil {
		return Report{}, e
	}
	if r, ok := c.cached(); ok {
		return r, nil
	}
	child, cancel := context.WithTimeout(ctx, CollectionTimeout)
	defer cancel()
	r := Report{Trust: "UNTRUSTED", CapturedAt: time.Now().UTC(), CacheTTLMS: CacheTTL.Milliseconds(), CollectionDeadlineMS: CollectionTimeout.Milliseconds(), Items: []Item{}}
	nextFailures := map[string]int{}
	for _, p := range c.probes {
		if e := ctx.Err(); e != nil {
			return Report{}, e
		}
		sample := Sample{State: "unknown"}
		if child.Err() == nil {
			budget := ProbeTimeout
			if p.Timeout > 0 {
				budget = p.Timeout
			}
			probeCtx, stop := context.WithTimeout(child, budget)
			v, e := p.Read(probeCtx)
			expired := probeCtx.Err() != nil
			stop()
			if e == nil && !expired && (v.State == "ok" || v.State == "degraded" || v.State == "unknown" || v.State == "stale") && len(v.Data) <= MaxProbeBytes && (len(v.Data) == 0 || json.Valid(v.Data)) {
				sample = v
			}
		}
		now := time.Now().UTC()
		observed := sample.ObservedAt
		if observed.IsZero() {
			observed = now
		}
		maxAge := sample.MaxAge
		if maxAge <= 0 {
			maxAge = CacheTTL
		}
		if observed.After(now.Add(time.Second)) || now.Sub(observed) > maxAge {
			sample.State = "stale"
		}
		action := sample.Action
		if action == nil {
			action = p.NextSafeAction
		}
		action = boundedAction(action)
		failures := 0
		if sample.State != "ok" {
			failures = c.failures[p.Name] + 1
			if failures > 255 {
				failures = 255
			}
		}
		item := Item{Name: p.Name, Sample: sample, CapturedAt: now, Failures: failures, Freshness: Freshness{ObservedAt: observed, AgeMS: now.Sub(observed).Milliseconds(), MaxAgeMS: maxAge.Milliseconds()}}
		item.Sample.Action = nil
		if sample.State != "ok" {
			item.NextSafeAction = action
		}
		r.Items = append(r.Items, item)
		raw, _ := json.Marshal(r)
		if len(raw) > MaxOutputBytes-256-(len(c.probes)-len(r.Items))*1024 {
			r.Items[len(r.Items)-1].Data = nil
			r.Items[len(r.Items)-1].State = "unknown"
			r.Items[len(r.Items)-1].NextSafeAction = action
			failures = c.failures[p.Name] + 1
			if failures > 255 {
				failures = 255
			}
			r.Items[len(r.Items)-1].Failures = failures
		}
		nextFailures[p.Name] = failures
	}
	if e := ctx.Err(); e != nil {
		return Report{}, e
	}
	c.refreshFreshness(&r)
	r.State, r.Counts = summarize(r.Items)
	r.Baseline = c.baseline(r.Items)
	// Freshness may add actions to formerly OK items at the end of collection.
	for i := len(r.Items) - 1; i >= 0; i-- {
		raw, _ := json.Marshal(r)
		reserve := 256
		for _, item := range r.Items {
			if item.NextSafeAction == nil {
				reserve += 512
			}
		}
		if len(raw)+reserve <= MaxOutputBytes {
			break
		}
		if len(r.Items[i].Data) == 0 {
			continue
		}
		r.Items[i].Data = nil
		r.Items[i].State = "unknown"
		r.Items[i].NextSafeAction = boundedAction(c.probes[i].NextSafeAction)
		if r.Items[i].Failures == 0 {
			r.Items[i].Failures = 1
		}
		r.State, r.Counts = summarize(r.Items)
	}
	for _, item := range r.Items {
		nextFailures[item.Name] = item.Failures
	}
	r.DurationMS = time.Since(r.CapturedAt).Milliseconds()
	c.failures = nextFailures
	c.mu.Lock()
	c.last = copyReport(r)
	c.cachedAt = time.Now()
	c.mu.Unlock()
	return copyReport(r), nil
}

// Last returns the most recent collection when it is younger than maxAge,
// without probing or waiting for a running collection; readers that must stay
// fast (tool_resolve) use it and report older evidence as not yet checked.
func (c *Collector) Last(maxAge time.Duration) (Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedAt.IsZero() || time.Since(c.cachedAt) >= maxAge {
		return Report{}, false
	}
	r := copyReport(c.last)
	r.Cached = true
	r.AgeMS = time.Since(r.CapturedAt).Milliseconds()
	c.refreshFreshness(&r)
	r.State, r.Counts = summarize(r.Items)
	return r, true
}
