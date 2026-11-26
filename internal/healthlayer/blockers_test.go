package healthlayer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNonOKItemsHaveSafeActionAndFreshness(t *testing.T) {
	c := New([]Probe{{Name: "missing", Read: func(context.Context) (Sample, error) { return Sample{State: "unknown"}, nil }}})
	r, err := c.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r.Items[0])
	var item map[string]any
	json.Unmarshal(raw, &item)
	if item["next_safe_action"] == nil || item["freshness"] == nil {
		t.Fatalf("missing actionable freshness: %s", raw)
	}
}

func TestFailureActionsSurviveErrorsAndOutputBudget(t *testing.T) {
	probes := []Probe{}
	for i := 0; i < MaxProbes; i++ {
		probes = append(probes, Probe{Name: fmt.Sprint(i), NextSafeAction: &Action{Tool: "host_triage", Args: json.RawMessage(`{}`), Approval: "none"}, Read: func(context.Context) (Sample, error) {
			return Sample{State: "ok", Data: json.RawMessage(`"` + strings.Repeat("x", 3900) + `"`)}, nil
		}})
	}
	r, err := New(probes).Read(context.Background())
	raw, _ := json.Marshal(r)
	if err != nil || len(raw) > MaxOutputBytes {
		t.Fatalf("output bound %d: %v", len(raw), err)
	}
	for _, item := range r.Items {
		if item.State != "ok" && item.NextSafeAction == nil {
			t.Fatal("output fallback lost action")
		}
	}
	c := New([]Probe{{Name: "error", Read: func(context.Context) (Sample, error) { return Sample{}, errors.New("no source") }}})
	r, err = c.Read(context.Background())
	if err != nil || r.Items[0].NextSafeAction == nil {
		t.Fatal("error lost action", err)
	}
	r.Items[0].NextSafeAction.Tool = "mutated"
	cached, _ := c.Read(context.Background())
	if cached.Items[0].NextSafeAction.Tool != "health_snapshot" {
		t.Fatal("cached action aliased")
	}
}

func TestEvidenceAgeKeepsSourcesIndependent(t *testing.T) {
	now := time.Now().UTC()
	probes := []Probe{}
	for _, name := range []string{"tunnel", "docker", "lane", "index"} {
		n := name
		probes = append(probes, Probe{Name: n, Read: func(context.Context) (Sample, error) {
			observed := now
			if n == "lane" || n == "index" {
				observed = now.Add(-time.Hour)
			}
			return Sample{State: "ok", ObservedAt: observed, MaxAge: 5 * time.Minute}, nil
		}})
	}
	r, err := New(probes).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "stale" || r.Items[0].State != "ok" || r.Items[1].State != "ok" || r.Items[2].State != "stale" || r.Items[3].State != "stale" {
		t.Fatalf("independence lost: %+v", r)
	}
}

func TestCachedEvidenceExpiresWithoutRepeatingProbe(t *testing.T) {
	calls := 0
	c := New([]Probe{{Name: "expiring", Read: func(context.Context) (Sample, error) {
		calls++
		return Sample{State: "ok", ObservedAt: time.Now(), MaxAge: time.Minute}, nil
	}}})
	first, err := c.Read(context.Background())
	if err != nil || first.State != "ok" {
		t.Fatal(first, err)
	}
	c.mu.Lock()
	c.last.Items[0].Freshness.ObservedAt = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	cached, err := c.Read(context.Background())
	if err != nil || !cached.Cached || cached.State != "stale" || cached.Items[0].NextSafeAction == nil || calls != 1 {
		t.Fatal(cached, err, calls)
	}
}
