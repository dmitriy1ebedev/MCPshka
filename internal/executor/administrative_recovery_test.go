package executor

import (
	"context"
	"errors"
	"mcpshka/internal/unitfence"
	"testing"
	"time"
)

type observationFence struct {
	auditedFence
	observations   int
	observationErr error
}

func (f *observationFence) VerifyStopped(context.Context, string, Identity) error {
	f.observations++
	return f.observationErr
}

func TestAdministrativeFenceRecoversStopBeforeDurableSnapshotByObservation(t *testing.T) {
	c, p, id := administrativeFixture(t)
	ctx := context.Background()
	if err := c.approveFence(ctx, p, id, "operator-request-42", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f := &observationFence{auditedFence: auditedFence{c: c, p: p, approval: id, fail: true}}
	if err := c.administrativeFence(ctx, p, id, f); !errors.Is(err, ErrHold) {
		t.Fatalf("unknown stop accepted: %v", err)
	}
	c = New(c.db)
	f.c = c
	if err := c.administrativeFence(ctx, p, id, f); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 || f.observations != 1 {
		t.Fatalf("recovery repeated stop or skipped proof: %+v", f)
	}
	s, err := c.Inspect(ctx, p)
	if err != nil || s.State != "FENCED" {
		t.Fatalf("lost recovery: %+v %v", s, err)
	}
	if err = c.administrativeFence(ctx, p, id, f); err != nil || f.calls != 1 || f.observations != 1 {
		t.Fatalf("replay mutated: %v %+v", err, f)
	}
}

func TestAdministrativeFenceObservationErrorKeepsHold(t *testing.T) {
	c, p, id := administrativeFixture(t)
	ctx := context.Background()
	if err := c.approveFence(ctx, p, id, "operator-request-42", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f := &observationFence{auditedFence: auditedFence{c: c, p: p, approval: id, fail: true}, observationErr: errors.New("replacement cgroup")}
	_ = c.administrativeFence(ctx, p, id, f)
	if err := c.administrativeFence(ctx, p, id, f); !errors.Is(err, ErrHold) {
		t.Fatalf("unverified recovery: %v", err)
	}
	s, err := c.Inspect(ctx, p)
	if err != nil || s.State != "REVOKING" || f.calls != 1 {
		t.Fatalf("unknown writer released: %+v %v calls=%d", s, err, f.calls)
	}
}

func TestRecoveryUnitRejectsNewInvocationAndRequiresAbsenceAfterReboot(t *testing.T) {
	c, p, _ := administrativeFixture(t)
	s, err := c.Inspect(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	i := *s.Launches[0].Identity
	stopped := "InvocationID=" + i.InvocationID + "\nControlGroup=\nMainPID=0\nRestart=no\nKillMode=control-group\nActiveState=inactive\nLoadState=loaded\n"
	absent := "InvocationID=\nControlGroup=\nMainPID=0\nRestart=no\nKillMode=control-group\nActiveState=inactive\nLoadState=not-found\n"
	if !unitfence.RecoveryUnitSafe(stopped, i, i.BootID) || !unitfence.RecoveryUnitSafe(absent, i, i.BootID) {
		t.Fatal("valid read-only unit proof rejected")
	}
	newBoot := "433f15c2-05e7-40dd-855e-d82b4c8d7e70"
	if unitfence.RecoveryUnitSafe(stopped, i, newBoot) || !unitfence.RecoveryUnitSafe(absent, i, newBoot) {
		t.Fatal("reboot requires absent unit")
	}
	i.InvocationID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if unitfence.RecoveryUnitSafe(stopped, i, i.BootID) || unitfence.RecoveryUnitSafe(absent, i, "garbage") {
		t.Fatal("replacement invocation accepted")
	}
	for _, events := range []string{"", "populated 1\n", "populated 0\npopulated 0\n", "populated 0 extra\n"} {
		if unitfence.EmptyCgroupEvents(events) {
			t.Fatalf("unsafe cgroup proof: %q", events)
		}
	}
	if !unitfence.EmptyCgroupEvents("populated 0\nfrozen 0\n") {
		t.Fatal("empty cgroup rejected")
	}
}
