package runnerci

import (
	"context"
	"mcpshka/internal/distributedci"
	"mcpshka/internal/runnerhub"
	"strings"
	"testing"
)

func TestQueuedShardHasPinnedIdentity(t *testing.T) {
	plan, err := distributedci.NewPlan(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), []string{"mcpshka/a", "mcpshka/b", "mcpshka/c"}, nil, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	hub := runnerhub.New(t.TempDir())
	p := &Provider{Root: "/root/repos/mcpshka", Hub: hub}
	for i, kind := range []string{"go-vet", "go-test"} {
		j, _ := runnerhub.Plan(p.Root, plan.Commit, kind, []string{"./a"}, 3600)
		j.ID, j.KeyHash = runnerhub.NewJobID(), runnerhub.KeyDigest(key(plan, 0, kind))
		if _, err := hub.Enqueue(j); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if o, found, err := p.Observe(context.Background(), plan, 0); err != nil || found || o.State == "PASS" {
				t.Fatal("partial enqueue must resume Start", o, found, err)
			}
		}
	}
	o, found, err := p.Observe(context.Background(), plan, 0)
	if err != nil || !found || o.State != "RUNNING" || plan.Check(o) != nil {
		t.Fatal(o, found, err)
	}
}
