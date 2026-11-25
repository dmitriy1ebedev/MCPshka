package forgeci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/distributedci"
	"mcpshka/internal/executor"
	"mcpshka/internal/gitprovider"
	"strconv"
	"time"
)

// Module status is read from root's existing fenced executor observations.
// It is for visibility only; root preparation independently verifies its permits.
func observeModule(ctx context.Context, r *distributedci.Report) {
	if r.State == "FAILED" || r.State == "HOLD" {
		return
	}
	if !brokerclient.Enabled() {
		return
	}
	for i, o := range r.Shards {
		for attempt := 9; attempt >= 1; attempt-- {
			seed := "integration-module:distributed-shard:" + strconv.Itoa(o.Shard) + ":" + Root + ":" + r.Plan.Commit
			if attempt > 1 {
				seed += ":attempt-" + strconv.Itoa(attempt)
			}
			sum := sha256.Sum256([]byte(seed))
			pin, launch, e := executor.DistributedShardIdentity(hex.EncodeToString(sum[:16]))
			if e != nil {
				continue
			}
			child, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			raw, e := brokerclient.Run(child, "executor", "status", pin.PermitID, launch)
			cancel()
			if e != nil {
				continue
			}
			var s executor.Snapshot
			if json.Unmarshal(raw, &s) != nil || s.Source == nil || s.Source.Repo != Root || s.Source.Commit != r.Plan.Commit || s.Source.Package != "module:shard-"+strconv.Itoa(o.Shard) {
				continue
			}
			if s.Result == nil {
				if o.Provider == "module" {
					r.Shards[i].State = "RUNNING"
					r.Shards[i].Seconds = max(0, time.Since(s.CreatedAt).Seconds())
				}
				break
			}
			m, e := gitprovider.ParseCIResult([]byte(s.Result.Output))
			if e != nil || r.Plan.Check(m) != nil || m.Provider != "module" || m.Shard != o.Shard {
				break
			}
			if m.State == "CODE_FAIL" {
				r.State = "FAILED"
				r.Shards[i] = m
				return
			}
			if executor.DistributedShardSucceeded(s) && m.State == "PASS" {
				r.Attempts = append(r.Attempts, m)
				if o.State != "PASS" {
					r.Shards[i] = m
				}
			}
			break
		}
	}
	all := len(r.Shards) == len(r.Plan.Shards)
	for _, o := range r.Shards {
		all = all && o.State == "PASS"
	}
	if all {
		r.State = "PASS"
	}
}
