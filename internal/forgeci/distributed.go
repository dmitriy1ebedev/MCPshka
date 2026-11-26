package forgeci

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/ciartifact"
	"mcpshka/internal/distributedci"
	"mcpshka/internal/gitprovider"
	"mcpshka/internal/opencore"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"time"
)

func runDistributed(ctx context.Context, mode, commit string) ([]byte, int, error) {
	if mode != "plan" && mode != "publish" && mode != "status" {
		return nil, 2, errors.New("distributed mode must be plan, publish or status")
	}
	var p distributedci.Plan
	var s *opencore.CISnapshot
	var e error
	if mode == "publish" {
		s, e = opencore.StageCI(ctx, Root, commit, opencore.HostRules(ctx))
		if e == nil {
			defer s.Close()
			p, e = distributedci.PlanPublicSnapshot(ctx, Root, commit, s)
		}
	} else {
		p, e = distributedci.LoadPublicPlan(ctx, Root, commit)
	}
	if e != nil {
		b, _ := json.Marshal(map[string]string{"state": "HOLD", "code": "DISTRIBUTED_PLAN_REFUSED", "detail": e.Error()})
		return b, 1, nil
	}
	if mode == "plan" {
		b, e := json.Marshal(p)
		return b, 0, e
	}
	providers := append(gitprovider.PublicCIProviders(Root, p, s), pullProvider(ctx, p, mode == "publish"))
	r := distributedci.Drive(ctx, p, providers, time.Now(), mode == "publish")
	observeModule(ctx, &r)
	if e := distributedci.SaveMetrics(r); e != nil {
		return nil, 2, e
	}
	b, e := json.Marshal(distributedStatus(r))
	code := 0
	if r.State == "FAILED" || r.State == "HOLD" {
		code = 1
	}
	return b, code, e
}

func distributedStatus(r distributedci.Report) map[string]any {
	rows := make([]map[string]any, 0, len(r.Shards))
	for _, o := range r.Shards {
		rows = append(rows, map[string]any{"shard": o.Shard, "provider": o.Provider, "state": o.State, "seconds": o.Seconds, "run_id": o.RunID, "failed_packages": o.FailedPackages})
	}
	return map[string]any{"schema": r.Schema, "state": r.State, "commit": r.Plan.Commit, "source_tree": r.Plan.Tree, "export_digest": r.Plan.Digest, "snapshot": r.Plan.Snapshot, "plan_hash": r.Plan.Hash(), "shards": rows, "table": r.Table(), "trust": "UNTRUSTED", "unavailable": r.Unavailable, "code": r.Code, "next_safe_action": r.NextSafeAction}
}

// RunProject uses the same shard planner and provider evidence for opt-in
// projects. H96 never runs missing project shards or builds.
func RunProject(ctx context.Context, mode, commit, root string) ([]byte, int, error) {
	if c := projectprofile.New(policy.Default()).CIFor(root); c == nil || c.Provider != "distributed" || !c.ExternalOnly {
		return nil, 2, errors.New("project has no distributed CI profile")
	}
	if mode != "plan" && mode != "publish" && mode != "status" && mode != "wait" {
		return nil, 2, errors.New("unsupported distributed project mode")
	}
	p, e := distributedci.LoadPlan(ctx, root, commit)
	if e != nil {
		return nil, 2, e
	}
	if mode == "plan" {
		b, e := json.Marshal(p)
		return b, 0, e
	}
	for {
		r := distributedci.Drive(ctx, p, gitprovider.DistributedProviders(root, p), time.Now(), mode == "publish" || mode == "wait")
		if r.State == "MODULE_REQUIRED" {
			r.State = "INFRA_FAIL"
		}
		if r.State == "PASS" {
			if e := collectProjectArtifacts(ctx, root, p, r); e != nil {
				return nil, 1, e
			}
		}
		if mode != "wait" || r.State != "RUNNING" {
			b, e := json.Marshal(r)
			code := 0
			if r.State == "FAILED" || r.State == "HOLD" || r.State == "INFRA_FAIL" {
				code = 1
			}
			return b, code, e
		}
		select {
		case <-ctx.Done():
			return nil, 2, ctx.Err()
		case <-time.After(20 * time.Second):
		}
	}
}

func collectProjectArtifacts(ctx context.Context, root string, p distributedci.Plan, r distributedci.Report) error {
	if e := r.Verify(p); e != nil {
		return e
	}
	for _, o := range r.Shards {
		if o.Shard == 0 {
			if len(o.Artifacts) == 0 {
				return errors.New("remote CI passed without build artifacts")
			}
			for _, provider := range gitprovider.DistributedProviders(root, p) {
				if provider.Name() == o.Provider {
					adapter := provider.(*gitprovider.CIProvider)
					return ciartifact.Store(root, p, o.Artifacts, func(a distributedci.Artifact) ([]byte, error) { return adapter.FetchArtifact(ctx, p, o, a) })
				}
			}
		}
	}
	return errors.New("remote build provider unavailable")
}
