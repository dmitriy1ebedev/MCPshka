package distributedci

import (
	"context"
	"testing"
	"time"
)

func TestOfflineQueuesReassignAllShardsToLiveSite(t *testing.T) {
	p := fixture(t)
	dead := &fakeSite{name: "gitverse", down: true, state: "QUEUED"}
	live := &fakeSite{name: "github"}
	r := Drive(context.Background(), p, []Provider{dead, live}, time.Now(), true)
	if r.State != "RUNNING" || live.starts != len(p.Shards) || len(r.Unavailable) != 1 {
		t.Fatal(r, live.starts)
	}
	for _, shard := range r.Shards {
		if shard.Provider != "github" {
			t.Fatal(shard)
		}
	}
	r = Drive(context.Background(), p, []Provider{dead}, time.Now(), true)
	if r.State != "MODULE_REQUIRED" || r.Attempts == nil || len(r.Unavailable) != 1 || r.Code != "NO_LIVE_CI_PROVIDER" || r.NextSafeAction == "" {
		t.Fatal(r)
	}
}
