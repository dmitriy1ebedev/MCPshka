package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type integrationRecoveryProbe struct {
	writerFenceErr error
	fenced         bool
	stoppedErr     error
	observeErr     error
	observation    integrationObservation
	stopped        int
	observed       int
}

func (p *integrationRecoveryProbe) WithWriterFence(_ context.Context, _ IntegrationApproval, fn func() error) error {
	if p.writerFenceErr != nil {
		return p.writerFenceErr
	}
	p.fenced = true
	defer func() { p.fenced = false }()
	return fn()
}

func (p *integrationRecoveryProbe) VerifyStopped(context.Context, string, Identity) error {
	if !p.fenced {
		return ErrHold
	}
	p.stopped++
	return p.stoppedErr
}

func (p *integrationRecoveryProbe) Observe(_ context.Context, _ IntegrationApproval) (integrationObservation, error) {
	p.observed++
	if p.stopped != 1 || !p.fenced {
		return integrationObservation{}, ErrHold
	}
	return p.observation, p.observeErr
}

func integrationSupervisorFixture(t *testing.T) (*Controller, IntegrationApproval, IntegrationClaim, Pin, string) {
	t.Helper()
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, m.Pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	repo := RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}
	a, err := c.recordIntegrationApproval(ctx, m, item, repo, IntegrationReview{Reviewer: "root-review", Digest: strings.Repeat("7", 64)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := c.reserveIntegrationClaim(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	pin := Pin{PermitID: strings.Repeat("8", 32), LeaseID: strings.Repeat("9", 32), ExecutionID: claim.ExecutionID, Generation: claim.Generation}
	launch := strings.Repeat("e", 32)
	if err := c.Create(pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Reserve(ctx, pin, launch); err != nil {
		t.Fatal(err)
	}
	if err := c.bindIntegrationWorker(ctx, claim, pin, launch); err != nil {
		t.Fatal(err)
	}
	if err := c.beginIntegrationEffect(ctx, claim); err != nil {
		t.Fatal(err)
	}
	return c, a, claim, pin, launch
}

func fenceIntegrationFixture(t *testing.T, c *Controller, p Pin, launch string) {
	t.Helper()
	ctx := context.Background()
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + launch + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	if err := c.Register(ctx, p, launch, i); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, p, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationSupervisorPendingLaunchNeverReleasesClaim(t *testing.T) {
	c, a, claim, _, _ := integrationSupervisorFixture(t)
	p := &integrationRecoveryProbe{observation: integrationObservation{Repository: a.Repository, Head: a.Request.Commit, Receipt: a.Request.Commit}}
	if err := New(c.db).recoverIntegrationClaim(context.Background(), a, p); !errors.Is(err, ErrHold) {
		t.Fatal("pending launch released", err)
	}
	if p.stopped != 0 || p.observed != 0 {
		t.Fatal("unknown worker was probed as a known incarnation")
	}
	got, _, err := c.readIntegrationClaim(context.Background(), claim.Repository.CommonDir)
	if err != nil || got.State != "EFFECT_INTENT" {
		t.Fatalf("intent lost: %+v %v", got, err)
	}
}

func TestIntegrationSupervisorRequiresFreshFenceBeforeGitProbe(t *testing.T) {
	for _, failure := range []string{"unfenced-writers", "live-worker", "git-unknown", "changed-inode", "target-drift", "missing-receipt"} {
		t.Run(failure, func(t *testing.T) {
			c, a, claim, pin, launch := integrationSupervisorFixture(t)
			fenceIntegrationFixture(t, c, pin, launch)
			p := &integrationRecoveryProbe{observation: integrationObservation{Repository: a.Repository, Head: a.Request.Commit, Receipt: a.Request.Commit}}
			switch failure {
			case "unfenced-writers":
				p.writerFenceErr = ErrHold
			case "live-worker":
				p.stoppedErr = ErrHold
			case "git-unknown":
				p.observeErr = ErrHold
			case "changed-inode":
				p.observation.Repository.Inode++
			case "target-drift":
				p.observation.Head = a.Request.TargetHead
			case "missing-receipt":
				p.observation.Receipt = ""
			}
			if err := New(c.db).recoverIntegrationClaim(context.Background(), a, p); !errors.Is(err, ErrHold) {
				t.Fatal("unverified recovery released", err)
			}
			if failure == "live-worker" && p.observed != 0 {
				t.Fatal("Git observed before process fence")
			}
			if failure == "unfenced-writers" && (p.stopped != 0 || p.observed != 0) {
				t.Fatal("probes ran without writer fence")
			}
			got, _, err := c.readIntegrationClaim(context.Background(), claim.Repository.CommonDir)
			if err != nil || got.State != "EFFECT_INTENT" {
				t.Fatalf("intent lost: %+v %v", got, err)
			}
		})
	}
}

func TestIntegrationSupervisorProductionCannotAssumeNativeWriterFence(t *testing.T) {
	called := false
	err := (liveIntegrationRecovery{}).WithWriterFence(context.Background(), IntegrationApproval{}, func() error { called = true; return nil })
	if !errors.Is(err, ErrHold) || called {
		t.Fatal("production writer fence was inferred", err)
	}
	c, a, claim, pin, launch := integrationSupervisorFixture(t)
	fenceIntegrationFixture(t, c, pin, launch)
	if err := New(c.db).recoverIntegrationClaim(context.Background(), a, liveIntegrationRecovery{}); !errors.Is(err, ErrHold) {
		t.Fatal("unavailable native barrier retired production claim", err)
	}
	current, _, err := c.readIntegrationClaim(context.Background(), claim.Repository.CommonDir)
	if err != nil || current.State != "EFFECT_INTENT" {
		t.Fatalf("unavailable writer barrier lost intent: %+v %v", current, err)
	}
}

func TestIntegrationSupervisorColdRecoveryRetiresOnlyFencedExactEffect(t *testing.T) {
	c, a, claim, pin, launch := integrationSupervisorFixture(t)
	fenceIntegrationFixture(t, c, pin, launch)
	p := &integrationRecoveryProbe{observation: integrationObservation{Repository: a.Repository, Head: a.Request.Commit, Receipt: a.Request.Commit}}
	if err := New(c.db).recoverIntegrationClaim(context.Background(), a, p); err != nil {
		t.Fatal(err)
	}
	if p.stopped != 1 || p.observed != 1 {
		t.Fatal("fresh trusted probes missing")
	}
	got, _, err := c.readIntegrationClaim(context.Background(), claim.Repository.CommonDir)
	if err != nil || got.State != "RELEASED" {
		t.Fatalf("verified effect not retired: %+v %v", got, err)
	}
	p = &integrationRecoveryProbe{stoppedErr: ErrHold, observeErr: ErrHold}
	if err := New(c.db).recoverIntegrationClaim(context.Background(), a, p); err != nil {
		t.Fatal("completed recovery not idempotent", err)
	}
	if p.stopped != 0 || p.observed != 0 {
		t.Fatal("completed recovery repeated external probes")
	}
}

func TestIntegrationSupervisorRejectsTestWorkerAndWrongGenerationBinding(t *testing.T) {
	c, a, claim, pin, launch := integrationSupervisorFixture(t)
	for _, wrong := range []Pin{a.TestPin, {PermitID: pin.PermitID, LeaseID: pin.LeaseID, ExecutionID: pin.ExecutionID, Generation: pin.Generation + 1}} {
		if err := c.bindIntegrationWorker(context.Background(), claim, wrong, launch); !errors.Is(err, ErrHold) {
			t.Fatal("wrong worker rebound", err)
		}
	}
}
