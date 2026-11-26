package executor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

func administrativeFixture(t *testing.T) (*Controller, Pin, string) {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1, ExecutionID: "cccccccccccccccccccccccccccccccc"}
	id := "dddddddddddddddddddddddddddddddd"
	if err = c.Create(p); err != nil {
		t.Fatal(err)
	}
	if err = c.Reserve(context.Background(), p, id); err != nil {
		t.Fatal(err)
	}
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: "ffffffffffffffffffffffffffffffff", Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	if err = c.Register(context.Background(), p, id, i); err != nil {
		t.Fatal(err)
	}
	return c, p, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
}

type auditedFence struct {
	c        *Controller
	p        Pin
	approval string
	calls    int
	fail     bool
}

func (f *auditedFence) StopAndVerify(ctx context.Context, _ string, _ Identity) error {
	r, _, err := f.c.readFenceApproval(ctx, f.p, f.approval)
	if err != nil || r.State != "FENCING" {
		return errors.New("external mutation before durable audit")
	}
	f.calls++
	if f.fail {
		return errors.New("unverified descendant")
	}
	return nil
}

func TestAdministrativeFenceAuditedAndReplayDoesNotStopAgain(t *testing.T) {
	c, p, approval := administrativeFixture(t)
	ctx := context.Background()
	if err := c.approveFence(ctx, p, approval, "operator-request-42", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f := &auditedFence{c: c, p: p, approval: approval}
	if err := c.administrativeFence(ctx, p, approval, f); err != nil {
		t.Fatal(err)
	}
	c = New(c.db) // Lost response and restarted controller.
	f.c = c
	if err := c.administrativeFence(ctx, p, approval, f); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Fatalf("replayed external stop: %d", f.calls)
	}
	if err := c.approveFence(ctx, p, approval, "operator-request-43", time.Now().Add(time.Minute)); !errors.Is(err, ErrHold) {
		t.Fatalf("approval ID reused: %v", err)
	}
}

func TestAdministrativeFenceUnknownDoesNotReplayMutation(t *testing.T) {
	c, p, approval := administrativeFixture(t)
	ctx := context.Background()
	if err := c.approveFence(ctx, p, approval, "operator-request-42", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f := &auditedFence{c: c, p: p, approval: approval, fail: true}
	if err := c.administrativeFence(ctx, p, approval, f); !errors.Is(err, ErrHold) {
		t.Fatalf("failed stop accepted: %v", err)
	}
	c = New(c.db)
	f.c = c
	f.fail = false
	if err := c.administrativeFence(ctx, p, approval, f); !errors.Is(err, ErrHold) {
		t.Fatalf("unknown result replayed: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("unknown mutation repeated: %d", f.calls)
	}
}

func TestAdministrativeFenceRejectsWrongGenerationMissingAndExpiredApproval(t *testing.T) {
	c, p, approval := administrativeFixture(t)
	ctx := context.Background()
	f := &auditedFence{c: c, p: p, approval: approval}
	if err := c.administrativeFence(ctx, p, approval, f); !errors.Is(err, ErrHold) {
		t.Fatalf("missing approval accepted: %v", err)
	}
	if err := c.approveFence(ctx, p, approval, "operator-request-42", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	q := p
	q.Generation++
	if err := c.administrativeFence(ctx, q, approval, f); !errors.Is(err, ErrHold) {
		t.Fatalf("wrong generation accepted: %v", err)
	}
	r, old, err := c.readFenceApproval(ctx, p, approval)
	if err != nil {
		t.Fatal(err)
	}
	r.ExpiresAt = time.Now().Add(-time.Minute)
	if err = c.replaceFenceApproval(approval, old, r); err != nil {
		t.Fatal(err)
	}
	if err = c.administrativeFence(ctx, p, approval, f); !errors.Is(err, ErrHold) {
		t.Fatalf("expired approval accepted: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("unauthorized external mutations: %d", f.calls)
	}
}

func TestUnprivilegedControllerCannotApproveOrExecuteAdministrativeFence(t *testing.T) {
	c, p, approval := administrativeFixture(t)
	if err := c.ApproveAdministrativeFence(context.Background(), p, approval, "operator-request-42", time.Now().Add(time.Minute)); !errors.Is(err, ErrHold) {
		t.Fatalf("non-authority approval: %v", err)
	}
	if err := c.AdministrativeFence(context.Background(), p, approval); !errors.Is(err, ErrHold) {
		t.Fatalf("non-authority fence: %v", err)
	}
}
