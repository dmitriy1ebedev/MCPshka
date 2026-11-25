package executor

import (
	"context"
	"mcpshka/internal/distributedci"
	"strconv"
	"strings"
)

const DistributedShardPurpose = "project-distributed-shard-v1"

func shardPackage(n int) string { return "module:shard-" + strconv.Itoa(n) }
func shardPackageValid(pkg string) bool {
	return pkg == "module:shard-0" || pkg == "module:shard-1" || pkg == "module:shard-2"
}
func DistributedShardCommand(p distributedci.Plan, n int) []string {
	return []string{"go", "run", "./cmd/ci-shard", strconv.Itoa(n), "module", p.Commit, p.Tree, p.Snapshot, p.Hash(), "1"}
}
func distributedShardCommandMatches(a []string) bool {
	return len(a) == 10 && strings.Join(a[:3], " ") == "go run ./cmd/ci-shard" && (a[3] == "0" || a[3] == "1" || a[3] == "2") && a[4] == "module" && commitPattern.MatchString(a[5]) && commitPattern.MatchString(a[6]) && commitPattern.MatchString(a[7]) && digestPattern.MatchString(a[8]) && a[9] == "1"
}
func PrepareDistributedShard(ctx context.Context, bootstrap, repo string, p distributedci.Plan, n int) (Manifest, error) {
	if n < 0 || n >= len(p.Shards) {
		return Manifest{}, ErrHold
	}
	return prepareSnapshotTest(ctx, bootstrap, repo, p.Commit, shardPackage(n), DistributedShardPurpose, ModuleTestProfile, DistributedShardCommand(p, n), 0)
}
func DistributedShardIdentity(bootstrap string) (Pin, string, error) {
	if !idPattern.MatchString(bootstrap) {
		return Pin{}, "", ErrHold
	}
	p, l := snapshotIdentity(DistributedShardPurpose, bootstrap)
	return p, l, nil
}

func DistributedShardSucceeded(s Snapshot) bool {
	return s.State == "FENCED" && s.Result != nil && s.Result.State == "SUCCEEDED" && s.Result.ExitCode == 0 && !s.Result.WaitFailed
}
