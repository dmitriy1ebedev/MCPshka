package distributedci

import (
	"context"
	"testing"
	"time"
)

// S5.FAST.5: every public site down → each shard is H96's (module), with an
// explicit code; a site that comes back takes its shard again.
func TestAllFourSitesDownLeavesEveryShardToH96(t *testing.T) {
	p := fixture(t)
	var sites []Provider
	var fakes []*fakeSite
	for _, n := range Sites {
		f := &fakeSite{name: n, down: true}
		fakes = append(fakes, f)
		sites = append(sites, f)
	}
	r := Drive(context.Background(), p, sites, time.Now(), true)
	if r.State != "MODULE_REQUIRED" || len(r.Unavailable) != len(Sites) {
		t.Fatal(r.State, r.Unavailable)
	}
	for _, o := range r.Shards {
		if o.Provider != "module" || o.State != "REQUIRED" {
			t.Fatal("shard not left to H96:", o)
		}
	}
	for _, f := range fakes {
		if f.starts != 0 {
			t.Fatal("started on a site that is down:", f.name)
		}
	}
	fakes[0].down, fakes[0].state = false, "RUNNING"
	if r = Drive(context.Background(), p, sites, time.Now(), true); r.State != "RUNNING" {
		t.Fatal("a returning site must take work again:", r.State)
	}
}
