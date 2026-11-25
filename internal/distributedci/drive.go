package distributedci

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Provider interface {
	Name() string
	Probe(context.Context) error
	Observe(context.Context, Plan, int) (Observation, bool, error)
	Start(context.Context, Plan, int) error
}

// Drive observes every site before selecting winners: a red package cannot
// be hidden by a later green retry. Only infrastructure failures fail over.
func Drive(ctx context.Context, p Plan, providers []Provider, day time.Time, start bool) Report {
	r := Report{Schema: Schema, Plan: p, State: "PASS", Trust: "UNTRUSTED", Attempts: []Observation{}}
	available := map[string]Provider{}
	observed := map[string]map[int]Observation{}
	for _, site := range providers {
		if err := site.Probe(ctx); err == nil {
			available[site.Name()] = site
		} else {
			r.Unavailable = append(r.Unavailable, Availability{Provider: site.Name(), Code: "CI_UNAVAILABLE", Detail: probeDetail(err)})
		}
		observed[site.Name()] = map[int]Observation{}
		for n := range p.Shards {
			o, found, e := site.Observe(ctx, p, n)
			if e != nil {
				if errors.Is(e, ErrEvidence) {
					r.State = "HOLD"
					return r
				}
				delete(available, site.Name())
				break
			}
			if !found {
				continue
			}
			if e = p.Check(o); e != nil {
				r.State = "HOLD"
				return r
			}
			if o.State == "QUEUED" && available[site.Name()] == nil {
				o.State = "INFRA_FAIL"
			}
			observed[site.Name()][n] = o
			r.Attempts = append(r.Attempts, o)
			if o.State == "CODE_FAIL" {
				r.Shards = append(r.Shards, o)
				r.State = "FAILED"
				return r
			}
		}
	}
	for n := range p.Shards {
		order := append(Assignment(n, day), "runner")
		var chosen *Observation
		// Preserve any valid success across date rotation and infrastructure retry.
		for _, site := range append(append([]string{}, Sites...), "runner") {
			if o, ok := observed[site][n]; ok && o.State == "PASS" {
				x := o
				chosen = &x
				break
			}
		}
		if chosen != nil {
			r.Shards = append(r.Shards, *chosen)
			continue
		}
		for _, site := range append(append([]string{}, Sites...), "runner") {
			if o, ok := observed[site][n]; ok && (o.State == "RUNNING" || o.State == "QUEUED" && available[site] != nil) {
				x := o
				chosen = &x
				break
			}
		}
		if chosen != nil {
			r.Shards = append(r.Shards, *chosen)
			r.State = "RUNNING"
			continue
		}
		selected := false
		for _, site := range order {
			adapter, ok := available[site]
			if !ok {
				continue
			}
			if o, ok := observed[site][n]; ok {
				if o.State == "INFRA_FAIL" {
					continue
				}
				r.Shards = append(r.Shards, o)
				selected = true
				break
			}
			o := Observation{Provider: site, Shard: n, State: "PENDING"}
			if start {
				if e := adapter.Start(ctx, p, n); e != nil {
					if errors.Is(e, ErrEvidence) {
						r.State = "HOLD"
						return r
					}
					if errors.Is(e, ErrDispatchUnknown) {
						o.State = "UNKNOWN"
					} else {
						delete(available, site)
						continue
					}
				} else {
					o.State = "RUNNING"
				}
			}
			r.Shards = append(r.Shards, o)
			selected = true
			break
		}
		if !selected {
			r.Shards = append(r.Shards, Observation{Provider: "module", Shard: n, State: "REQUIRED"})
			if r.State == "PASS" {
				r.State = "MODULE_REQUIRED"
			}
		} else {
			r.State = "RUNNING"
		}
	}
	if r.State == "MODULE_REQUIRED" {
		r.Code = "NO_LIVE_CI_PROVIDER"
		r.NextSafeAction = "restore a configured CI provider and retry distributed CI; for an explicit bounded H96 check use project_test local=true with packages/run"
	}
	return r
}
func (r Report) Table() string {
	s := "shard\tprovider\tstate\tseconds\n"
	for _, o := range r.Shards {
		s += fmt.Sprintf("%d\t%s\t%s\t%.3f\n", o.Shard, o.Provider, o.State, o.Seconds)
	}
	return s
}

// probeDetail keeps the first 200 bytes of a probe error on one line.
func probeDetail(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
