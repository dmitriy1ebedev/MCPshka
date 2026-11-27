package distributedgate

import (
	"context"
	"errors"
	"fmt"
	"mcpshka/internal/cievidence"
	"mcpshka/internal/distributedci"
	"mcpshka/internal/executor"
	"mcpshka/internal/gitprovider"
	"strconv"
	"time"
)

func DistributedEvidenceBootstrap(repo, commit, attempt string) string {
	return CIEvidenceBootstrap("distributed:"+repo, commit, attempt)
}
func distributedFallback(ctx context.Context, runExecutor func(context.Context, []string) ([]byte, error), repo, attempt string, p distributedci.Plan, n int) (distributedci.Observation, bool, error) {
	bootstrap := ModuleTestBootstrap("distributed-shard:"+strconv.Itoa(n)+":"+repo, p.Commit, attempt)
	pin, launch, e := executor.DistributedShardIdentity(bootstrap)
	if e != nil {
		return distributedci.Observation{}, false, e
	}
	c, m, e := executor.OpenPermit(pin.PermitID)
	if e != nil {
		m, e = executor.PrepareDistributedShard(ctx, bootstrap, repo, p, n)
		if e != nil {
			return distributedci.Observation{}, false, e
		}
		c, m, e = executor.OpenPermit(m.Pin.PermitID)
		if e != nil {
			return distributedci.Observation{}, false, e
		}
	}
	if m.Purpose != executor.DistributedShardPurpose || m.Source == nil || m.Source.Repo != repo || m.Source.Commit != p.Commit {
		return distributedci.Observation{}, false, errors.New("module shard manifest mismatch")
	}
	s, e := c.Inspect(ctx, m.Pin)
	if e != nil {
		return distributedci.Observation{}, false, e
	}
	if len(s.Launches) == 0 {
		_, e = runExecutor(ctx, []string{"launch", pin.PermitID, launch})
		return distributedci.Observation{}, false, e
	}
	if s.Result == nil {
		return distributedci.Observation{}, false, nil
	}
	if s.State != "FENCED" {
		if _, e = runExecutor(ctx, []string{"canary-fence", pin.PermitID, launch}); e != nil {
			return distributedci.Observation{}, false, e
		}
		s, e = c.Inspect(ctx, m.Pin)
		if e != nil || s.State != "FENCED" {
			return distributedci.Observation{}, false, executor.ErrHold
		}
	}
	o, e := gitprovider.ParseCIResult([]byte(s.Result.Output))
	if e != nil || p.Check(o) != nil || o.Provider != "module" || o.Shard != n || o.State == "PASS" && !executor.DistributedShardSucceeded(s) {
		return o, false, errors.New("module shard result mismatch")
	}
	return o, true, nil
}

// Root independently re-reads provider APIs; the service journal and forge_ci
// result are never accepted as evidence. H96 executes one missing shard at a time.
func Prepare(ctx context.Context, repo, commit, attempt string, runExecutor func(context.Context, []string) ([]byte, error)) (executor.Manifest, error) {
	p, e := distributedci.LoadPublicPlan(ctx, repo, commit)
	if e != nil {
		return executor.Manifest{}, e
	}
	local := map[int]distributedci.Observation{}
	for {
		r := distributedci.Drive(ctx, p, gitprovider.DistributedEvidenceProviders(repo, p), time.Now(), false)
		if r.State == "FAILED" || r.State == "HOLD" {
			return executor.Manifest{}, fmt.Errorf("distributed CI %s: %s", r.State, r.Table())
		}
		pending := false
		for i, o := range r.Shards {
			if o.State == "PASS" {
				continue
			}
			if l, ok := local[o.Shard]; ok {
				r.Shards[i] = l
				continue
			}
			if o.Provider != "module" {
				pending = true
				continue
			}
			l, done, e := distributedFallback(ctx, runExecutor, repo, attempt, p, o.Shard)
			if e != nil {
				return executor.Manifest{}, e
			}
			if !done {
				pending = true
				break
			}
			if l.State == "CODE_FAIL" {
				return executor.Manifest{}, fmt.Errorf("FAILED: %v on module", l.FailedPackages)
			}
			if l.State != "PASS" {
				return executor.Manifest{}, errors.New("module shard infrastructure failure")
			}
			local[o.Shard] = l
			r.Shards[i] = l
		}
		if !pending && len(r.Shards) == len(p.Shards) {
			r.State = "PASS"
			if e = r.Verify(p); e != nil {
				return executor.Manifest{}, e
			}
			tree, e := distributedci.CanonicalTree(ctx, repo, commit)
			if e != nil {
				return executor.Manifest{}, e
			}
			ev := cievidence.Evidence{Schema: cievidence.Schema, Provider: "distributed", Repository: "distributed:mcpshka", HeadSHA: commit, Tree: tree, BaseSHA: p.Snapshot, Distributed: &r, CheckedAt: time.Now().UTC()}
			return executor.PrepareCIEvidence(ctx, DistributedEvidenceBootstrap(repo, commit, attempt), repo, commit, ev)
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return executor.Manifest{}, ctx.Err()
		case <-timer.C:
		}
	}
}
