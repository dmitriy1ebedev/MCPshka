package distributedci

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSite struct {
	name   string
	down   bool
	state  string
	tamper bool
	starts int
}

func (f *fakeSite) Name() string { return f.name }
func (f *fakeSite) Probe(context.Context) error {
	if f.down {
		return errors.New("offline")
	}
	return nil
}
func (f *fakeSite) Observe(_ context.Context, p Plan, n int) (Observation, bool, error) {
	if f.state == "" {
		return Observation{}, false, nil
	}
	o := pass(p, n, f.name)
	o.State = f.state
	if o.State == "CODE_FAIL" {
		o.FailedPackages = o.Packages
	}
	if f.tamper {
		o.Tree = p.Commit
	}
	return o, true, nil
}
func (f *fakeSite) Start(context.Context, Plan, int) error { f.starts++; return nil }
func TestUnavailableAndInfraReassignOnlyAffectedShard(t *testing.T) {
	p := fixture(t)
	d := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	order := Assignment(0, d)
	for _, infra := range []bool{false, true} {
		a := &fakeSite{name: order[0], down: !infra}
		if infra {
			a.state = "INFRA_FAIL"
		}
		b := &fakeSite{name: order[1], state: "PASS"}
		c := &fakeSite{name: order[2], down: true}
		r := Drive(context.Background(), p, []Provider{a, b, c}, d, true)
		if e := r.Verify(p); e != nil {
			t.Fatal(e)
		}
		for _, o := range r.Shards {
			if o.Provider != b.name {
				t.Fatal(o)
			}
		}
	}
}
func TestCodeFailureAndTamperNeverFallBack(t *testing.T) {
	p := fixture(t)
	for _, tamper := range []bool{false, true} {
		a := &fakeSite{name: "github", state: "CODE_FAIL", tamper: tamper}
		b := &fakeSite{name: "gitverse", state: "PASS"}
		c := &fakeSite{name: "moshub", state: "PASS"}
		r := Drive(context.Background(), p, []Provider{a, b, c}, time.Now(), true)
		if r.State == "PASS" || b.starts != 0 || c.starts != 0 {
			t.Fatal(r)
		}
	}
}
func TestModuleOnlyAfterEveryRemoteFails(t *testing.T) {
	p := fixture(t)
	a := &fakeSite{name: "github", state: "INFRA_FAIL"}
	b := &fakeSite{name: "gitverse", down: true}
	c := &fakeSite{name: "moshub", state: "INFRA_FAIL"}
	r := Drive(context.Background(), p, []Provider{a, b, c}, time.Now(), true)
	if r.State != "MODULE_REQUIRED" {
		t.Fatal(r.State)
	}
	for _, o := range r.Shards {
		if o.Provider != "module" {
			t.Fatal(o)
		}
	}
	a.down = false
	a.state = "RUNNING"
	r = Drive(context.Background(), p, []Provider{a, b, c}, time.Now(), true)
	if r.State != "RUNNING" {
		t.Fatal(r.State)
	}
}

func TestRotationPreservesInFlightShard(t *testing.T) {
	p := fixture(t)
	d := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	old := Assignment(0, d)[0]
	a := &fakeSite{name: old, state: "RUNNING"}
	others := []*fakeSite{}
	providers := []Provider{a}
	for _, site := range Sites {
		if site != old {
			f := &fakeSite{name: site}
			others = append(others, f)
			providers = append(providers, f)
		}
	}
	r := Drive(context.Background(), p, providers, d.Add(24*time.Hour), true)
	if r.State != "RUNNING" {
		t.Fatal(r)
	}
	for _, f := range others {
		if f.starts != 0 {
			t.Fatal("rotation duplicated an in-flight shard", f.name, f.starts)
		}
	}
}

func TestQueuedShardSurvivesRotation(t *testing.T) {
	p := fixture(t)
	d := time.Now().UTC()
	a := &fakeSite{name: "github", state: "QUEUED"}
	b := &fakeSite{name: "gitverse"}
	c := &fakeSite{name: "moshub"}
	r := Drive(context.Background(), p, []Provider{a, b, c}, d.Add(24*time.Hour), true)
	if r.State != "RUNNING" || b.starts+c.starts != 0 {
		t.Fatal("queued shard dispatched twice", r)
	}
}

func TestFourSitesReceiveShareAndOfflineQueueReassigns(t *testing.T) {
	p := fixture(t)
	p.Shards = append(p.Shards, []string{"mcpshka/e"})
	p.Packages = append(p.Packages, "mcpshka/e")
	providers := []Provider{}
	sites := []*fakeSite{}
	for _, name := range Sites {
		f := &fakeSite{name: name}
		sites = append(sites, f)
		providers = append(providers, f)
	}
	r := Drive(context.Background(), p, providers, time.Now(), true)
	if len(r.Shards) != 4 || len(Sites) != 4 {
		t.Fatal(r)
	}
	for _, f := range sites {
		if f.starts != 1 {
			t.Fatalf("%s received %d shards", f.name, f.starts)
		}
	}
	a := &fakeSite{name: "moshub", down: true, state: "QUEUED"}
	b := &fakeSite{name: "sourcecraft"}
	r = Drive(context.Background(), p, []Provider{a, b}, time.Now(), true)
	if b.starts != 4 {
		t.Fatal("offline queue stranded shards", r)
	}
}
