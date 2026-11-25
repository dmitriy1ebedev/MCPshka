package cievidence

import (
	"mcpshka/internal/distributedci"
	"strings"
	"testing"
	"time"
)

func TestDistributedEvidenceRequiresCompleteCoverage(t *testing.T) {
	p, e := distributedci.NewPlan(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), []string{"mcpshka/a", "mcpshka/b", "mcpshka/c"}, nil, nil, 3)
	if e != nil {
		t.Fatal(e)
	}
	r := distributedci.Report{Schema: distributedci.Schema, Plan: p, State: "PASS"}
	for n := range p.Shards {
		r.Shards = append(r.Shards, distributedci.Observation{Provider: "gitverse", Shard: n, Commit: p.Commit, Tree: p.Tree, Snapshot: p.Snapshot, PlanHash: p.Hash(), Packages: p.Shards[n], State: "PASS", RunID: 1})
	}
	ev := Evidence{Schema: Schema, Provider: "distributed", Repository: "distributed:mcpshka", HeadSHA: p.Commit, Tree: p.Tree, BaseSHA: p.Snapshot, Distributed: &r, CheckedAt: time.Now()}
	if e = ev.Verify(p.Commit, p.Tree); e != nil {
		t.Fatal(e)
	}
	r.Shards = r.Shards[:2]
	if ev.Verify(p.Commit, p.Tree) == nil {
		t.Fatal("incomplete evidence passed")
	}
}
