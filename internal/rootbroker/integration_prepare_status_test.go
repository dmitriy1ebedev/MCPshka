package rootbroker

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/executor"
	"mcpshka/internal/statestore"
)

func TestSealedModulePrepareReportsPendingFence(t *testing.T) {
	ctx := context.Background()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := executor.New(db)
	p := executor.Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	id := strings.Repeat("d", 32)
	identity := executor.Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	for _, err := range []error{c.Create(p), c.Reserve(ctx, p, id), c.Register(ctx, p, id, identity)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	m := executor.Manifest{Pin: p, Purpose: executor.ModuleTestPurpose, Runs: map[string]executor.RunSpec{id: {}}}
	check := func(want string) {
		t.Helper()
		raw, err := sealedModulePrepareState(ctx, c, m, id)
		if err != nil {
			t.Fatal(err)
		}
		var state modulePrepareState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if state.State != "SEALED" || state.PermitID != p.PermitID || state.LaunchID != id || state.NextAction != want || state.Executor == nil || state.Executor.NextAction != want {
			t.Fatalf("prepare status: %s", raw)
		}
	}
	check("")
	result, _ := json.Marshal(executor.ExecutionResult{Pin: p, LaunchID: id, Identity: identity, ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"})
	if ok, err := db.PutIfAbsent("executor/results/"+p.PermitID+".json", result); err != nil || !ok {
		t.Fatalf("record: %v", err)
	}
	check("canary-fence " + p.PermitID + " " + id)
	if err := c.Fence(ctx, p, prepareStatusFence{}); err != nil {
		t.Fatal(err)
	}
	check("")
}

type prepareStatusFence struct{}

func (prepareStatusFence) StopAndVerify(context.Context, string, executor.Identity) error { return nil }

func TestPrepareUnitStatus(t *testing.T) {
	for _, tc := range []struct{ name, props, want string }{
		{"running", "LoadState=loaded\nActiveState=active\nSubState=running\nResult=success\nExecMainStatus=0", "PREPARING"},
		{"queued", "LoadState=loaded\nActiveState=activating\nSubState=start\nResult=success\nExecMainStatus=0", "PREPARING"},
		{"failed", "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=exit-code\nExecMainStatus=1", "FAILED"},
		{"timeout", "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=timeout\nExecMainStatus=0", "FAILED"},
		{"exited", "LoadState=loaded\nActiveState=inactive\nSubState=dead\nResult=success\nExecMainStatus=0", "FAILED"},
		{"retained", "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainStatus=0", "FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, found, err := prepareUnitState(modulePrepareState{State: "PREPARING"}, "2", []byte(tc.props))
			if err != nil || !found || out.State != tc.want {
				t.Fatalf("state %+v found %v err %v", out, found, err)
			}
			if tc.want == "FAILED" && (out.Reason == "" || !strings.Contains(out.NextAction, "attempt 3")) {
				t.Fatalf("missing recovery: %+v", out)
			}
		})
	}
	out, found, err := prepareUnitState(modulePrepareState{State: "PREPARING"}, "1", []byte("LoadState=not-found\nActiveState=inactive"))
	if err != nil || found || out.State != "PREPARING" {
		t.Fatalf("absent: %+v %v %v", out, found, err)
	}
	if _, _, err := prepareUnitState(modulePrepareState{}, "1", []byte("garbage")); err == nil {
		t.Fatal("malformed status accepted")
	}
	out, _, err = prepareUnitState(modulePrepareState{}, "9", []byte("LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=/secret/token\nExecMainStatus=1"))
	if err != nil || strings.Contains(out.Reason, "secret") || strings.Contains(out.NextAction, "attempt 10") {
		t.Fatalf("unsafe result: %+v %v", out, err)
	}
}
