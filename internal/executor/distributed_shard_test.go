package executor

import (
	"mcpshka/internal/distributedci"
	"strings"
	"testing"
)

func TestDistributedShardCannotAuthorizeWholeModule(t *testing.T) {
	p, e := distributedci.NewPlan(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), []string{"mcpshka/a", "mcpshka/b", "mcpshka/c"}, nil, nil, 3)
	if e != nil {
		t.Fatal(e)
	}
	cmd := DistributedShardCommand(p, 1)
	spec := RunSpec{Profile: ModuleTestProfile, Command: cmd, WorkspaceDevice: 1, WorkspaceInode: 1}
	if e := spec.validate(); e != nil {
		t.Fatal(e)
	}
	m := Manifest{Purpose: DistributedShardPurpose, Source: &SourceSnapshot{Package: shardPackage(1), Commit: p.Commit}}
	if integrationTestPurpose(m.Purpose) || integrationTestRun(m, spec) {
		t.Fatal("partial shard authorized full integration")
	}
	for _, i := range []int{0, 2, 3, 4, 5, 8, 9} {
		bad := append([]string(nil), cmd...)
		bad[i] = "bad"
		if distributedShardCommandMatches(bad) {
			t.Fatal("invalid shard command", i)
		}
	}
	if moduleTestCommandMatches(cmd) {
		t.Fatal("partial command accepted as whole module")
	}
}

func TestDistributedShardRequiresSuccessfulFencedWait(t *testing.T) {
	good := Snapshot{State: "FENCED", Result: &ExecutionResult{State: "SUCCEEDED", ExitCode: 0}}
	if !DistributedShardSucceeded(good) {
		t.Fatal("successful fenced shard rejected")
	}
	for _, kind := range []string{"active", "wait", "failed", "exit", "missing"} {
		s := good
		r := *good.Result
		s.Result = &r
		switch kind {
		case "active":
			s.State = "ACTIVE"
		case "wait":
			r.WaitFailed = true
		case "failed":
			r.State = "FAILED"
		case "exit":
			r.ExitCode = 1
		case "missing":
			s.Result = nil
		}
		if DistributedShardSucceeded(s) {
			t.Fatal("incomplete worker accepted", kind)
		}
	}
}
