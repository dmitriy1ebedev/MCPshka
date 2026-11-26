package gitprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/distributedci"
)

type ciRun struct {
	ID        int64     `json:"id"`
	HeadSHA   string    `json:"head_sha"`
	CommitSHA string    `json:"commit_sha"`
	SHA       string    `json:"sha"`
	Ref       string    `json:"ref"`
	Branch    string    `json:"head_branch"`
	Status    string    `json:"status"`
	Created   time.Time `json:"created_at"`
	Event     string    `json:"event"`
	Path      string    `json:"path"`
}
type ciJob struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	Failure    string    `json:"failure_reason"`
	Started    time.Time `json:"started_at"`
	Finished   time.Time `json:"completed_at"`
	Commit     struct {
		ID string `json:"id"`
	} `json:"commit"`
	Pipeline struct {
		ID int64 `json:"id"`
	} `json:"pipeline"`
}

func (p *CIProvider) Observe(ctx context.Context, plan distributedci.Plan, n int) (distributedci.Observation, bool, error) {
	if !p.loaded {
		p.loaded = true
		p.cache = map[int]distributedci.Observation{}
		if p.Client.site == SourceCraft {
			p.loadErr = p.loadSourceCraft(ctx, plan)
		} else {
			p.loadErr = p.load(ctx, plan)
		}
	}
	if p.loadErr != nil {
		return distributedci.Observation{}, false, p.loadErr
	}
	o, ok := p.cache[n]
	journal := filepath.Join(p.journalDir(plan.Commit), p.Name()+"-"+strconv.Itoa(n))
	if ok {
		if o.State == "RUNNING" || o.State == "QUEUED" {
			if b, e := os.ReadFile(journal); e == nil {
				if stamp, e := time.Parse(time.RFC3339Nano, strings.SplitN(string(b), "\n", 2)[0]); e == nil {
					o.Seconds = min(6*3600, max(0, time.Since(stamp).Seconds()))
					limit := 50 * time.Minute
					if o.State == "QUEUED" {
						limit = 15 * time.Minute
					}
					if time.Since(stamp) > limit {
						o.State = "INFRA_FAIL"
					}
				}
			}
		}
		return o, true, nil
	}
	b, e := os.ReadFile(journal)
	if e == nil {
		stamp, e := time.Parse(time.RFC3339Nano, strings.SplitN(string(b), "\n", 2)[0])
		if e != nil {
			return o, false, distributedci.ErrEvidence
		}
		o = distributedci.Observation{Provider: p.Name(), Shard: n, Commit: plan.Commit, Tree: plan.Tree, Snapshot: plan.Snapshot, PlanHash: plan.Hash(), Packages: plan.Shards[n], State: "QUEUED", RunID: 1, Seconds: min(6*3600, max(0, time.Since(stamp).Seconds()))}
		if time.Since(stamp) > 15*time.Minute {
			o.State = "INFRA_FAIL"
		}
		return o, true, nil
	}
	if !os.IsNotExist(e) {
		return o, false, e
	}
	return o, false, nil
}
func (p *CIProvider) load(ctx context.Context, plan distributedci.Plan) error {
	if !p.public {
		return p.loadRef(ctx, plan, distributedci.Ref(plan), -1)
	}
	for n := range plan.Shards {
		if err := p.loadRef(ctx, plan, distributedci.ShardRef(plan, n), n); err != nil {
			return err
		}
	}
	return nil
}

func (p *CIProvider) loadRef(ctx context.Context, plan distributedci.Plan, branch string, shard int) error {
	c := p.Client
	route := p.route()
	var runs []ciRun
	if c.site == MosHub {
		if e := c.ciJSON(ctx, "GET", route+"/pipelines?ref="+url.QueryEscape(branch)+"&per_page=100", nil, &runs); e != nil {
			return e
		}
	} else {
		var data struct {
			Runs         []ciRun `json:"runs"`
			WorkflowRuns []ciRun `json:"workflow_runs"`
		}
		runsPath := route + "/actions/workflows/" + ciWorkflow + "/runs"
		if p.public && c.site == GitHub {
			runsPath = route + "/actions/runs"
		}
		if e := c.ciJSON(ctx, "GET", runsPath+"?per_page=100&branch="+url.QueryEscape(branch), nil, &data); e != nil {
			return e
		}
		runs = data.WorkflowRuns
		if c.site == GitVerse && len(data.Runs) > 0 {
			runs = data.Runs
		}
	}
	for _, r := range runs {
		if p.public && c.site == GitHub && strings.SplitN(r.Path, "@", 2)[0] != ".github/workflows/"+ciWorkflow {
			continue
		}
		sha, ref := r.HeadSHA, r.Branch
		if c.site == GitVerse {
			sha, ref = r.CommitSHA, strings.TrimPrefix(r.Ref, "refs/heads/")
		}
		if c.site == MosHub {
			sha, ref = r.SHA, r.Ref
		}
		if ref != branch {
			continue
		}
		if sha != plan.Snapshot || r.ID <= 0 {
			return distributedci.ErrEvidence
		}
		var jobs []ciJob
		if c.site == MosHub {
			if e := c.ciJSON(ctx, "GET", fmt.Sprintf("%s/pipelines/%d/jobs?per_page=100&include_retried=false", route, r.ID), nil, &jobs); e != nil {
				return e
			}
		} else {
			var data struct {
				Jobs []ciJob `json:"jobs"`
			}
			if e := c.ciJSON(ctx, "GET", fmt.Sprintf("%s/actions/runs/%d/jobs?per_page=100", route, r.ID), nil, &data); e != nil {
				return e
			}
			jobs = data.Jobs
		}
		for _, j := range jobs {
			n := -1
			for i := range plan.Shards {
				if j.Name == fmt.Sprintf("distributed-shard-%d", i) {
					n = i
				}
			}
			if shard >= 0 && j.Name == "distributed-shard" {
				n = shard
			}
			if n < 0 || shard >= 0 && shard != n {
				continue
			}
			if j.ID <= 0 {
				return distributedci.ErrEvidence
			}
			if c.site == MosHub && (j.Commit.ID != plan.Snapshot || j.Pipeline.ID != r.ID) {
				return distributedci.ErrEvidence
			}
			o := distributedci.Observation{Provider: p.Name(), Shard: n, Commit: plan.Commit, Tree: plan.Tree, Snapshot: plan.Snapshot, PlanHash: plan.Hash(), Packages: plan.Shards[n], State: "RUNNING", RunID: r.ID}
			terminal := j.Status == "completed" || j.Status == "success" || j.Status == "failure" || j.Status == "failed" || j.Status == "cancelled" || j.Status == "canceled" || j.Status == "skipped"
			if !terminal {
				limit := 50 * time.Minute
				switch j.Status {
				case "queued", "pending", "waiting", "created", "waiting_for_resource", "preparing":
					o.State = "QUEUED"
					limit = 15 * time.Minute
				}
				if !r.Created.IsZero() && time.Since(r.Created) > limit {
					o.State = "INFRA_FAIL"
				}
			} else {
				path := fmt.Sprintf("%s/actions/jobs/%d/logs", route, j.ID)
				if c.site == MosHub {
					path = fmt.Sprintf("%s/jobs/%d/trace", route, j.ID)
				}
				raw, e := c.ciRequest(ctx, "GET", path, nil)
				if e != nil {
					return e
				}
				parsed, e := ParseCIResult(raw)
				if e == nil {
					if parsed.Provider != p.Name() || parsed.RunID != r.ID || parsed.Shard != n || plan.Check(parsed) != nil {
						return distributedci.ErrEvidence
					}
					success := j.Status == "success" || j.Conclusion == "success"
					if parsed.State == "PASS" && !success {
						return distributedci.ErrEvidence
					}
					o = parsed
				} else if j.Status == "cancelled" || j.Status == "canceled" || j.Conclusion == "cancelled" || j.Conclusion == "timed_out" || j.Conclusion == "startup_failure" || j.Conclusion == "action_required" || j.Failure == "runner_system_failure" || j.Failure == "stuck_or_timeout_failure" || CIInfraLog(raw) {
					o.State = "INFRA_FAIL"
				} else if j.Status == "failure" || j.Status == "failed" || j.Conclusion == "failure" {
					// A failed job that never printed its result died before the
					// shard ran (bootstrap, toolchain): move the shard elsewhere.
					// It can never become PASS, so this cannot hide a code failure
					// — the shard is still tested on another site or on H96.
					o.State = "INFRA_FAIL"
				} else {
					// Success without a result line is not trustworthy.
					return distributedci.ErrEvidence
				}
			}
			prev, exists := p.cache[n]
			// Any code failure wins across attempts; otherwise newest run wins.
			if !exists || o.State == "CODE_FAIL" || prev.State != "CODE_FAIL" && o.RunID > prev.RunID {
				p.cache[n] = o
			}
		}
	}
	if c.site == GitHub && !p.public {
		for _, o := range p.cache {
			if o.State == "RUNNING" || o.State == "QUEUED" {
				p.quotaRemaining -= 50
			}
		}
	}
	return nil
}
func ParseCIResult(raw []byte) (distributedci.Observation, error) {
	var o distributedci.Observation
	found := false
	for _, line := range bytes.Split(raw, []byte("\n")) {
		i := bytes.Index(line, []byte(distributedci.ResultMarker))
		if i < 0 {
			continue
		}
		if found {
			return o, errors.New("duplicate CI result")
		}
		found = true
		d := json.NewDecoder(bytes.NewReader(line[i+len(distributedci.ResultMarker):]))
		d.DisallowUnknownFields()
		if d.Decode(&o) != nil || d.Decode(new(any)) != io.EOF {
			return o, errors.New("malformed CI result")
		}
	}
	if !found {
		return o, errors.New("CI result missing")
	}
	return o, nil
}
func CIInfraLog(raw []byte) bool {
	for _, s := range []string{"dial tcp", "TLS handshake timeout", "no space left on device", "runner system failure", "Failed to pull image", "Cannot connect to the Docker daemon", "Temporary failure resolving", "Connection timed out", "Failed to fetch", "Could not resolve host", "CI infrastructure:", "Job execution failed (system failure)"} {
		if bytes.Contains(raw, []byte(s)) {
			return true
		}
	}
	return false
}
