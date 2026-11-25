package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestIntegrationWorkerAuthenticatesExactSealedAdmission(t *testing.T) {
	c, a := integrationLaunchFixture(t)
	ctx := context.Background()
	m, err := c.prepareIntegrationLaunch(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + m.LaunchID + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	if err := c.recordIntegrationDispatch(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := c.Register(ctx, m.Pin, m.LaunchID, i); err != nil {
		t.Fatal(err)
	}
	if err := c.authenticateIntegrationWorker(ctx, m, a, i); !errors.Is(err, ErrHold) {
		t.Fatal("unverified import accepted", err)
	}
	imported := ProtectedImport{Version: 1, ApprovalID: a.ID, Repository: a.Repository, Candidate: a.Request.Commit, Target: a.Request.TargetHead, State: "VERIFIED"}
	raw, _ := json.Marshal(imported)
	if err := c.db.Put(protectedImportKey(a.ID), raw); err != nil {
		t.Fatal(err)
	}
	if err := c.authenticateIntegrationWorker(ctx, m, a, i); err != nil {
		t.Fatal(err)
	}
	other := i
	other.StartTicks++
	if err := c.authenticateIntegrationWorker(ctx, m, a, other); !errors.Is(err, ErrHold) {
		t.Fatal("wrong incarnation admitted", err)
	}
	if err := c.db.Put(protectedImportKey(a.ID), append(raw, []byte(" {}")...)); err != nil {
		t.Fatal(err)
	}
	if err := c.authenticateIntegrationWorker(ctx, m, a, i); !errors.Is(err, ErrHold) {
		t.Fatal("trailing import data accepted", err)
	}
	if err := c.db.Put(protectedImportKey(a.ID), raw); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.authenticateIntegrationWorker(ctx, m, a, i); !errors.Is(err, ErrHold) {
		t.Fatal("closed permit admitted", err)
	}
}
func TestIntegrationWorkerRejectsStaleClaimAndCorruptBinding(t *testing.T) {
	for _, kind := range []string{"generation", "binding"} {
		t.Run(kind, func(t *testing.T) {
			c, a := integrationLaunchFixture(t)
			ctx := context.Background()
			m, err := c.prepareIntegrationLaunch(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + m.LaunchID + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
			if err := c.recordIntegrationDispatch(ctx, m); err != nil {
				t.Fatal(err)
			}
			if err := c.Register(ctx, m.Pin, m.LaunchID, i); err != nil {
				t.Fatal(err)
			}
			imported := ProtectedImport{Version: 1, ApprovalID: a.ID, Repository: a.Repository, Candidate: a.Request.Commit, Target: a.Request.TargetHead, State: "VERIFIED"}
			raw, _ := json.Marshal(imported)
			if err := c.db.Put(protectedImportKey(a.ID), raw); err != nil {
				t.Fatal(err)
			}
			if kind == "binding" {
				if err := c.db.Put(integrationWorkerKey(a.ExecutionID), []byte("{}")); err != nil {
					t.Fatal(err)
				}
			} else {
				claim := m.Claim
				claim.Generation++
				raw, _ := json.Marshal(claim)
				if err := c.db.Put(integrationClaimKey(a.Repository.CommonDir), raw); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.authenticateIntegrationWorker(ctx, m, a, i); !errors.Is(err, ErrHold) {
				t.Fatal("stale or corrupt authority accepted", err)
			}
		})
	}
}
