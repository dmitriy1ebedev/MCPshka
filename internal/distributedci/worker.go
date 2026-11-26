package distributedci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

var ErrInfrastructure = errors.New("CI infrastructure failure")

const ResultMarker = "MCPSHKA_SHARD_RESULT="

type CheckRunner func(context.Context, string, []string) ([]byte, error)

func checkRun(ctx context.Context, dir string, args []string) ([]byte, error) {
	c := exec.CommandContext(ctx, "go", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOMAXPROCS=2")
	var b capBuffer
	b.limit = 4 << 20
	c.Stdout = &b
	c.Stderr = &b
	e := c.Run()
	var startErr *exec.Error
	if errors.As(e, &startErr) {
		return nil, ErrInfrastructure
	}
	if b.exceeded {
		return nil, ErrInfrastructure
	}
	return b.Bytes(), e
}
func RunShard(ctx context.Context, dir string, p Plan, n int, provider string, runID int64, run CheckRunner) Observation {
	o := Observation{Provider: provider, Shard: n, Commit: p.Commit, Tree: p.Tree, Snapshot: p.Snapshot, PlanHash: p.Hash(), State: "INFRA_FAIL", RunID: runID}
	start := time.Now()
	if n < 0 || n >= len(p.Shards) {
		return o
	}
	o.Packages = slices.Clone(p.Shards[n])
	if run == nil {
		run = checkRun
	}
	for _, args := range [][]string{append([]string{"vet", "-p", "1"}, o.Packages...), append([]string{"test", "-json", "-count=1", "-p", "1", "-timeout=20m"}, o.Packages...)} {
		raw, e := run(ctx, dir, args)
		if e == nil {
			continue
		}
		o.Seconds = time.Since(start).Seconds()
		failedTest := false
		for _, line := range bytes.Split(raw, []byte("\n")) {
			var ev struct{ Action, Test string }
			if json.Unmarshal(line, &ev) == nil && ev.Action == "fail" && ev.Test != "" {
				failedTest = true
			}
		}
		if !failedTest && (ctx.Err() != nil || errors.Is(e, ErrInfrastructure) || infraOutput(string(raw))) {
			return o
		}
		o.State = "CODE_FAIL"
		for _, line := range bytes.Split(raw, []byte("\n")) {
			var event struct{ Action, Package string }
			if json.Unmarshal(line, &event) == nil && event.Action == "fail" && slices.Contains(o.Packages, event.Package) && !slices.Contains(o.FailedPackages, event.Package) {
				o.FailedPackages = append(o.FailedPackages, event.Package)
			}
		}
		if len(o.FailedPackages) == 0 {
			o.FailedPackages = slices.Clone(o.Packages)
		}
		return o
	}
	o.State = "PASS"
	o.Seconds = time.Since(start).Seconds()
	return o
}
func infraOutput(s string) bool {
	for _, v := range []string{"dial tcp", "TLS handshake timeout", "no space left on device", "resource temporarily unavailable", "signal: killed", "failed to download", "proxyconnect tcp"} {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

// Worker recomputes the package inventory and plan inside the snapshot. External
// jobs also verify Git identity; the module worker relies on its sealed manifest.
func Worker(ctx context.Context, dir string, n int, site, commit, tree, snapshot, hash string, id int64, moduleDigest ...string) (Observation, error) {
	started := time.Now()
	digest := ""
	if len(moduleDigest) != 0 {
		digest = moduleDigest[0]
	}
	if site != "module" {
		c, t, s, e := identity(ctx, dir)
		if e != nil || c != commit || t != tree || s != snapshot {
			return Observation{}, errors.New("CI checkout identity mismatch")
		}
		digest, e = checkoutDigest(ctx, dir)
		if e != nil {
			return Observation{}, e
		}
	}
	packages, counts, weights, e := inventory(ctx, dir)
	if e != nil {
		return Observation{}, e
	}
	p, e := NewPlan(commit, tree, snapshot, packages, weights, counts, len(Sites))
	p.Digest = digest
	if e != nil || p.Hash() != hash || n < 0 || n >= len(p.Shards) {
		return Observation{}, errors.New("CI shard plan mismatch")
	}
	o := RunShard(ctx, dir, p, n, site, id, nil)
	if o.State == "PASS" {
		if e := projectChecks(ctx, dir, n); e != nil {
			if errors.Is(e, ErrInfrastructure) {
				o.State = "INFRA_FAIL"
			} else {
				o.State = "CODE_FAIL"
				o.FailedPackages = o.Packages
			}
		}
	}
	if o.State == "PASS" && n == 0 {
		artifacts, e := buildProject(ctx, dir)
		if e != nil {
			if errors.Is(e, ErrInfrastructure) {
				o.State = "INFRA_FAIL"
			} else {
				o.State = "CODE_FAIL"
				o.FailedPackages = o.Packages
			}
		} else {
			o.Artifacts = artifacts
		}
	}
	o.Seconds = time.Since(started).Seconds()
	return o, p.Check(o)
}

// SourceCraftWorker derives the immutable identity locally; no API run fields
// are trusted as source identity. The result artifact binds the full plan.
func SourceCraftWorker(ctx context.Context, dir, ref string) (Observation, error) {
	return sourceCraftWorker(ctx, dir, ref, false)
}
func SourceCraftStarted(ctx context.Context, dir, ref string) (Observation, error) {
	return sourceCraftWorker(ctx, dir, ref, true)
}
func sourceCraftWorker(ctx context.Context, dir, ref string, started bool) (Observation, error) {
	return PushWorker(ctx, dir, ref, "sourcecraft", 1, started)
}

// PushWorker derives shard and plan identity from its immutable branch.
func PushWorker(ctx context.Context, dir, ref, site string, id int64, started bool) (Observation, error) {
	c, t, s, e := identity(ctx, dir)
	if e != nil {
		return Observation{}, e
	}
	digest, e := checkoutDigest(ctx, dir)
	if e != nil {
		return Observation{}, e
	}
	p := Plan{Commit: c, Digest: digest}
	ref = strings.TrimPrefix(ref, "refs/heads/")
	branch, shard, ok := strings.Cut(ref, "/shard-")
	if !ok || !ValidShardIndex(shard) || digest == "" && site != "sourcecraft" {
		return Observation{}, errors.New("invalid shard ref")
	}
	n, _ := strconv.Atoi(shard)
	if ref != ShardRef(p, n) || branch == "" {
		return Observation{}, errors.New("invalid shard ref")
	}
	packages, counts, weights, e := inventory(ctx, dir)
	if e != nil {
		return Observation{}, e
	}
	p, e = NewPlan(c, t, s, packages, weights, counts, len(Sites))
	p.Digest = digest
	if e != nil {
		return Observation{}, e
	}
	if started {
		return Observation{Provider: site, Shard: n, Commit: c, Tree: t, Snapshot: s, PlanHash: p.Hash(), Packages: p.Shards[n], State: "RUNNING", RunID: id}, nil
	}
	return Worker(ctx, dir, n, site, c, t, s, p.Hash(), id)
}
