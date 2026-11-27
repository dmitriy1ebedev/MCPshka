package distributedci

import (
	"slices"
	"testing"
	"time"
)

func fixture(t *testing.T) Plan {
	t.Helper()
	p, e := NewPlan("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccccccccccc", []string{"mcpshka/a", "mcpshka/b", "mcpshka/c", "mcpshka/d"}, map[string]float64{"mcpshka/a": 10}, map[string]int{"mcpshka/b": 4}, 3)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func pass(p Plan, n int, site string) Observation {
	return Observation{Provider: site, Shard: n, Commit: p.Commit, Tree: p.Tree, Snapshot: p.Snapshot, PlanHash: p.Hash(), Packages: slices.Clone(p.Shards[n]), State: "PASS", RunID: 1}
}
func TestPlanDeterministicAndRotation(t *testing.T) {
	p := fixture(t)
	q, e := NewPlan(p.Commit, p.Tree, p.Snapshot, []string{"mcpshka/d", "mcpshka/c", "mcpshka/b", "mcpshka/a"}, map[string]float64{"mcpshka/a": 10}, map[string]int{"mcpshka/b": 4}, 3)
	if e != nil || p.Hash() != q.Hash() {
		t.Fatal("unstable plan")
	}
	d := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	if Assignment(0, d)[0] == Assignment(0, d.Add(24*time.Hour))[0] {
		t.Fatal("no rotation")
	}
}
func TestEvidenceRejectsTamperingAndMissingCoverage(t *testing.T) {
	p := fixture(t)
	r := Report{Schema: Schema, Plan: p, State: "PASS"}
	for i := range p.Shards {
		r.Shards = append(r.Shards, pass(p, i, Sites[i]))
	}
	if e := r.Verify(p); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"tree", "snapshot", "packages", "missing", "duplicate", "code"} {
		t.Run(kind, func(t *testing.T) {
			x := r
			x.Shards = slices.Clone(r.Shards)
			switch kind {
			case "tree":
				x.Shards[0].Tree = p.Commit
			case "snapshot":
				x.Shards[0].Snapshot = p.Tree
			case "packages":
				x.Shards[0].Packages = []string{"mcpshka/other"}
			case "missing":
				x.Shards = x.Shards[:2]
			case "duplicate":
				x.Shards[1] = x.Shards[0]
			case "code":
				o := pass(p, 0, "moshub")
				o.State = "CODE_FAIL"
				o.FailedPackages = o.Packages
				x.Attempts = []Observation{o}
			}
			if x.Verify(p) == nil {
				t.Fatal("accepted", kind)
			}
		})
	}
}
