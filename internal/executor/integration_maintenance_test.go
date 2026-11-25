package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type integrationMaintenanceFixture struct {
	runtime                                 IntegrationRuntime
	stopped                                 bool
	success                                 bool
	ambiguous                               bool
	stops, observations, recoveries, aborts int
	retirements                             int
	retireErr                               error
}

func (b *integrationMaintenanceFixture) Status(context.Context, string) (IntegrationRuntime, error) {
	return b.runtime, nil
}
func (b *integrationMaintenanceFixture) Fence(context.Context, string) (IntegrationRuntime, error) {
	b.stops++
	b.stopped = true
	b.runtime.Worker.State = "FENCED"
	return b.runtime, nil
}
func (b *integrationMaintenanceFixture) RecoverFence(context.Context, string) (IntegrationRuntime, error) {
	b.observations++
	if !b.stopped {
		return IntegrationRuntime{}, ErrHold
	}
	b.runtime.Worker.State = "FENCED"
	return b.runtime, nil
}
func (b *integrationMaintenanceFixture) Recover(context.Context, string) (IntegrationClaim, error) {
	b.recoveries++
	if !b.success {
		return IntegrationClaim{}, ErrHold
	}
	b.runtime.Claim.State = "RELEASED"
	return b.runtime.Claim, nil
}
func (b *integrationMaintenanceFixture) Abort(context.Context, string) (IntegrationClaim, error) {
	b.aborts++
	if b.ambiguous {
		return IntegrationClaim{}, ErrHold
	}
	b.runtime.Claim.State = "FAILED"
	return b.runtime.Claim, nil
}
func (b *integrationMaintenanceFixture) Retire(context.Context, string) error {
	b.retirements++
	return b.retireErr
}
func integrationMaintenanceTestFixture() (*integrationMaintenanceFixture, string) {
	id := strings.Repeat("a", 64)
	claim := IntegrationClaim{Version: 1, ApprovalID: id, Repository: RepositoryIdentity{CommonDir: "/root/repos/protected.git", Device: 1, Inode: 2}, ExecutionID: strings.Repeat("c", 32), Candidate: strings.Repeat("d", 40), Owner: "ours", Generation: 2, CreatedAt: time.Now().UTC(), State: "HELD"}
	pin := Pin{PermitID: strings.Repeat("e", 32), LeaseID: strings.Repeat("f", 32), ExecutionID: claim.ExecutionID, Generation: claim.Generation}
	launchID := strings.Repeat("b", 32)
	identity := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("1", 32), Cgroup: "/system.slice/mcpshka-executor-" + launchID + ".service", CgroupDevice: 3, CgroupInode: 4, PID: 50, StartTicks: 60}
	runtime := IntegrationRuntime{Claim: claim, Launch: IntegrationLaunch{Version: 1, ApprovalID: id, Claim: claim, Pin: pin, LaunchID: launchID, State: "READY"}, Worker: Snapshot{Pin: pin, State: "ACTIVE", Launches: []Launch{{ID: launchID, State: "REGISTERED", Identity: &identity}}}, Dispatched: true}
	return &integrationMaintenanceFixture{runtime: runtime}, id
}
func integrationMaintenanceResult(b *integrationMaintenanceFixture) {
	l := b.runtime.Worker.Launches[0]
	b.runtime.Worker.Result = &ExecutionResult{Pin: b.runtime.Worker.Pin, LaunchID: l.ID, Identity: *l.Identity, ExitCode: 0, ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}
}
func TestIntegrationMaintenanceLeavesLiveWorkerAndUnknownAdmission(t *testing.T) {
	b, id := integrationMaintenanceTestFixture()
	if err := maintainIntegrationWorker(context.Background(), id, b); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	if b.stops != 0 || b.observations != 1 || b.recoveries != 0 || b.aborts != 0 {
		t.Fatalf("live worker signalled or completed: %+v", b)
	}
	b.runtime.Dispatched = false
	if err := maintainIntegrationWorker(context.Background(), id, b); !errors.Is(err, ErrHold) {
		t.Fatal("undispatched admission continued")
	}
	if b.observations != 1 || b.stops != 0 {
		t.Fatal("undispatched worker probed or signalled")
	}
}
func TestIntegrationMaintenanceResultThenFenceThenGitCompletion(t *testing.T) {
	b, id := integrationMaintenanceTestFixture()
	integrationMaintenanceResult(b)
	b.success = true
	if err := maintainIntegrationWorker(context.Background(), id, b); err != nil {
		t.Fatal(err)
	}
	if b.stops != 1 || b.recoveries != 1 || b.observations != 0 || b.aborts != 0 || b.runtime.Claim.State != "RELEASED" {
		t.Fatal("terminal integration did not reconcile exact fence/Git")
	}
	// A new maintenance invocation observes root's durable terminal claim.
	if err := maintainIntegrationWorker(context.Background(), id, b); err != nil {
		t.Fatal(err)
	}
	if b.stops != 1 || b.recoveries != 1 {
		t.Fatal("terminal mutation repeated")
	}
	if b.retirements != 2 {
		t.Fatal("released integration did not run idempotent retirement", b.retirements)
	}
}

func TestIntegrationMaintenanceRetirementHoldNeverUndoesCompletion(t *testing.T) {
	b, id := integrationMaintenanceTestFixture()
	b.runtime.Claim.State = "RELEASED"
	b.retireErr = ErrHold
	if err := maintainIntegrationWorker(context.Background(), id, b); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	if b.retirements != 1 || b.recoveries != 0 || b.stops != 0 || b.runtime.Claim.State != "RELEASED" {
		t.Fatal("cleanup hold changed completed integration", b)
	}
	b.runtime.Claim.State = "FAILED"
	if err := maintainIntegrationWorker(context.Background(), id, b); err != nil {
		t.Fatal(err)
	}
	if b.retirements != 1 {
		t.Fatal("failed branch was retired")
	}
}
func TestIntegrationMaintenanceLostExitDoesNotManufactureSuccess(t *testing.T) {
	b, id := integrationMaintenanceTestFixture()
	b.stopped = true
	if err := maintainIntegrationWorker(context.Background(), id, b); err != nil {
		t.Fatal(err)
	}
	if b.stops != 0 || b.observations != 1 || b.recoveries != 1 || b.aborts != 1 || b.runtime.Claim.State != "FAILED" || b.runtime.Worker.Result != nil {
		t.Fatal("lost exit invented success or signal")
	}
}
func TestIntegrationMaintenanceAmbiguousEffectStaysHold(t *testing.T) {
	b, id := integrationMaintenanceTestFixture()
	b.stopped = true
	b.ambiguous = true
	if err := maintainIntegrationWorker(context.Background(), id, b); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	if b.runtime.Claim.State != "HELD" || b.stops != 0 {
		t.Fatal("ambiguous effect retired or replayed")
	}
}
func TestIntegrationMaintenanceRejectsForgedResultAndWrongGeneration(t *testing.T) {
	for _, mut := range []func(*integrationMaintenanceFixture){
		func(b *integrationMaintenanceFixture) { b.runtime.Worker.Result.Pin.Generation-- },
		func(b *integrationMaintenanceFixture) { b.runtime.Worker.Result.Identity.StartTicks++ },
		func(b *integrationMaintenanceFixture) { b.runtime.Worker.Result.Trust = "TRUSTED" },
		func(b *integrationMaintenanceFixture) { b.runtime.Claim.Generation++ },
		func(b *integrationMaintenanceFixture) { b.runtime.Launch.ApprovalID = strings.Repeat("0", 64) },
	} {
		b, id := integrationMaintenanceTestFixture()
		integrationMaintenanceResult(b)
		mut(b)
		if err := maintainIntegrationWorker(context.Background(), id, b); !errors.Is(err, ErrHold) {
			t.Fatal("forged evidence accepted")
		}
		if b.stops != 0 || b.observations != 0 || b.recoveries != 0 {
			t.Fatal("bad evidence reached external mutation")
		}
	}
}
func TestIntegrationMaintenanceCancelledSweepHasNoEffects(t *testing.T) {
	b, id := integrationMaintenanceTestFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := maintainIntegrationWorker(ctx, id, b); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b.stops != 0 || b.observations != 0 || b.recoveries != 0 {
		t.Fatal("cancelled sweep continued")
	}
}
