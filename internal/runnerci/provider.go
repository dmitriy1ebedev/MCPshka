// Package runnerci lends unclaimed distributed shards to the existing pull queue.
package runnerci

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"mcpshka/internal/distributedci"
	"mcpshka/internal/offbox"
	"mcpshka/internal/runnerhub"
)

type Provider struct {
	Root   string
	Hub    *runnerhub.Hub
	Driver *offbox.Driver
}

func (p *Provider) Name() string                                           { return "runner" }
func (p *Provider) Key(plan distributedci.Plan, n int, kind string) string { return key(plan, n, kind) }
func (p *Provider) Probe(context.Context) error {
	if p.Driver.Wake == nil && p.Hub.Fit("go") != "" {
		return errors.New("no pull runner or waker")
	}
	return nil
}
func key(plan distributedci.Plan, n int, kind string) string {
	return fmt.Sprintf("distributed:%s:%d:%s", plan.Hash(), n, kind)
}
func (p *Provider) Start(ctx context.Context, plan distributedci.Plan, n int) error {
	packages := make([]string, len(plan.Shards[n]))
	for i, pkg := range plan.Shards[n] {
		prefix := filepath.Base(p.Root) + "/"
		if !strings.HasPrefix(pkg, prefix) {
			return distributedci.ErrEvidence
		}
		packages[i] = "./" + strings.TrimPrefix(pkg, prefix)
	}
	for _, kind := range []string{"go-vet", "go-test"} {
		k := key(plan, n, kind)
		job, ok := p.Hub.ByKey(k)
		if !ok {
			var err error
			job, err = runnerhub.Plan(p.Root, plan.Commit, kind, packages, 3600)
			if err != nil {
				return err
			}
			job.ID, job.KeyHash, job.Workdir = runnerhub.NewJobID(), runnerhub.KeyDigest(k), p.Root
			if err := p.Hub.Archive(ctx, job); err != nil {
				return err
			}
			job, err = p.Hub.Enqueue(job)
			if err != nil {
				return err
			}
		}
		p.Driver.Advance(ctx, job.ID)
	}
	return nil
}
func (p *Provider) Observe(_ context.Context, plan distributedci.Plan, n int) (distributedci.Observation, bool, error) {
	runID, _ := strconv.ParseInt(plan.Hash()[:15], 16, 64)
	o := distributedci.Observation{Provider: p.Name(), Shard: n, Commit: plan.Commit, Tree: plan.Tree, Snapshot: plan.Snapshot, PlanHash: plan.Hash(), Packages: plan.Shards[n], RunID: runID, State: "PASS"}
	found, complete := false, true
	for _, kind := range []string{"go-vet", "go-test"} {
		j, ok := p.Hub.ByKey(key(plan, n, kind))
		if !ok {
			complete = false
			o.State = "QUEUED"
			continue
		}
		found = true
		if j.Project != p.Root || j.Commit != plan.Commit {
			return o, false, distributedci.ErrEvidence
		}
		if j.State == runnerhub.Failed && j.Error == "" {
			o.State, o.FailedPackages = "CODE_FAIL", plan.Shards[n]
			return o, true, nil
		}
		if j.State == runnerhub.NoRunner || j.Error != "" {
			o.State = "INFRA_FAIL"
			return o, true, nil
		}
		if j.State != runnerhub.Done {
			o.State = "RUNNING"
		}
		if !j.Finished.IsZero() {
			o.Seconds += min(3*3600, max(0, j.Finished.Sub(j.Created).Seconds()))
		}
	}
	return o, found && complete, nil
}
