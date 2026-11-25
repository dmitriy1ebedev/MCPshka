package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func integrationLaunchFixture(t *testing.T) (*Controller, IntegrationApproval) {
	t.Helper()
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	if e := c.Close(ctx, m.Pin); e != nil {
		t.Fatal(e)
	}
	if e := c.Fence(ctx, m.Pin, &fenceStub{}); e != nil {
		t.Fatal(e)
	}
	a, e := c.recordIntegrationApproval(ctx, m, item, RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}, IntegrationReview{Reviewer: "root-review", Digest: strings.Repeat("7", 64)})
	if e != nil {
		t.Fatal(e)
	}
	return c, a
}
func TestIntegrationLaunchSealedPreparationReplay(t *testing.T) {
	c, a := integrationLaunchFixture(t)
	ctx := context.Background()
	m, e := c.prepareIntegrationLaunch(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	n, e := New(c.db).prepareIntegrationLaunch(ctx, a)
	if e != nil || m != n {
		t.Fatal("cold replay changed authority", e)
	}
	s, e := c.Inspect(ctx, m.Pin)
	if e != nil || len(s.Launches) != 1 || s.Launches[0].State != "LAUNCH_INTENT" || s.Launches[0].Identity != nil {
		t.Fatal("prepared process identity fabricated", e)
	}
	if _, e := c.db.Get(integrationWorkerKey(a.ExecutionID)); e != nil {
		t.Fatal("binding absent", e)
	}
}
func TestIntegrationLaunchCannotReplayAfterRegistrationOrClose(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "registered", true: "closed"}[closed], func(t *testing.T) {
			c, a := integrationLaunchFixture(t)
			ctx := context.Background()
			m, e := c.prepareIntegrationLaunch(ctx, a)
			if e != nil {
				t.Fatal(e)
			}
			if closed {
				e = c.Close(ctx, m.Pin)
			} else {
				e = c.Register(ctx, m.Pin, m.LaunchID, Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + m.LaunchID + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4})
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e := c.prepareIntegrationLaunch(ctx, a); !errors.Is(e, ErrHold) {
				t.Fatal("unknown launch retried", e)
			}
		})
	}
}
func TestIntegrationLaunchRecoversSplitPermitCreationOnlyWithExactIntent(t *testing.T) {
	c, a := integrationLaunchFixture(t)
	ctx := context.Background()
	claim, e := c.reserveIntegrationClaim(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	p := Pin{PermitID: integrationLaunchIdentity("permit", claim), LeaseID: integrationLaunchIdentity("lease", claim), Generation: claim.Generation, ExecutionID: claim.ExecutionID}
	intent := IntegrationLaunch{Version: 1, ApprovalID: a.ID, Claim: claim, Pin: p, LaunchID: integrationLaunchIdentity("launch", claim), State: "PREPARE_INTENT"}
	raw, _ := json.Marshal(intent)
	if e := c.db.Put(integrationLaunchKey(p.PermitID), raw); e != nil {
		t.Fatal(e)
	}
	binding, _ := json.Marshal(p)
	if e := c.db.Put(generationKey(p), binding); e != nil {
		t.Fatal(e)
	}
	m, e := c.prepareIntegrationLaunch(ctx, a)
	if e != nil || m.State != "READY" {
		t.Fatal("split prepare not recovered", e)
	}
}
func TestIntegrationLaunchRejectsWrongGenerationBinding(t *testing.T) {
	c, a := integrationLaunchFixture(t)
	ctx := context.Background()
	claim, e := c.reserveIntegrationClaim(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	p := Pin{PermitID: integrationLaunchIdentity("permit", claim), LeaseID: integrationLaunchIdentity("lease", claim), Generation: claim.Generation, ExecutionID: claim.ExecutionID}
	other := p
	other.PermitID = strings.Repeat("6", 32)
	raw, _ := json.Marshal(other)
	if e := c.db.Put(generationKey(p), raw); e != nil {
		t.Fatal(e)
	}
	if _, e := c.prepareIntegrationLaunch(ctx, a); !errors.Is(e, ErrHold) {
		t.Fatal("foreign binding replaced", e)
	}
	got, _ := c.db.Get(generationKey(p))
	if string(got) != string(raw) {
		t.Fatal("foreign generation lost")
	}
}

func TestIntegrationLaunchRecoversSplitLaunchReservation(t *testing.T) {
	c, a := integrationLaunchFixture(t)
	ctx := context.Background()
	claim, e := c.reserveIntegrationClaim(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	pin := Pin{PermitID: integrationLaunchIdentity("permit", claim), LeaseID: integrationLaunchIdentity("lease", claim), Generation: claim.Generation, ExecutionID: claim.ExecutionID}
	intent := IntegrationLaunch{Version: 1, ApprovalID: a.ID, Claim: claim, Pin: pin, LaunchID: integrationLaunchIdentity("launch", claim), State: "PREPARE_INTENT"}
	raw, _ := json.Marshal(intent)
	if e := c.db.Put(integrationLaunchKey(pin.PermitID), raw); e != nil {
		t.Fatal(e)
	}
	if e := c.Create(pin); e != nil {
		t.Fatal(e)
	}
	binding, _ := json.Marshal(pin)
	if e := c.db.Put("executor/launches/"+intent.LaunchID+".json", binding); e != nil {
		t.Fatal(e)
	}
	got, e := c.prepareIntegrationLaunch(ctx, a)
	if e != nil || got.State != "READY" {
		t.Fatal("split reservation not recovered", e)
	}
	s, e := c.Inspect(ctx, pin)
	if e != nil || len(s.Launches) != 1 || s.Launches[0].State != "LAUNCH_INTENT" || s.Launches[0].Identity != nil {
		t.Fatal("bad reconstructed admission", e)
	}
}
func TestIntegrationLaunchReadyCannotRepairLostAuthority(t *testing.T) {
	c, a := integrationLaunchFixture(t)
	ctx := context.Background()
	m, e := c.prepareIntegrationLaunch(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	// Corruption is simulated as a different complete snapshot, not by deleting
	// live authority or touching any production fixture.
	raw, _ := json.Marshal(Snapshot{Pin: m.Pin, Revision: 1, State: "ACTIVE", Launches: []Launch{}})
	if e := c.db.Put(key(m.Pin), raw); e != nil {
		t.Fatal(e)
	}
	if _, e := c.prepareIntegrationLaunch(ctx, a); !errors.Is(e, ErrHold) {
		t.Fatal("READY recreated admission", e)
	}
	s, e := c.Inspect(ctx, m.Pin)
	if e != nil || len(s.Launches) != 0 {
		t.Fatal("lost snapshot repaired", e)
	}
}
