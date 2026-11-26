package executor

import (
	"context"
	"errors"
	"fmt"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recoverFenceBackend struct {
	stopped      bool
	observations int
	stops        int
}

func (b *recoverFenceBackend) StopAndVerify(context.Context, string, Identity) error {
	b.stops++
	return ErrHold
}
func (b *recoverFenceBackend) VerifyStopped(context.Context, string, Identity) error {
	b.observations++
	if b.stopped {
		return nil
	}
	return ErrHold
}

func TestRecoverExpiredWorkerNeverRepeatsStop(t *testing.T) {
	ctx := context.Background()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	id := strings.Repeat("d", 32)
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	for _, e := range []error{c.Create(p), c.Reserve(ctx, p, id), c.Register(ctx, p, id, i)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	b := &recoverFenceBackend{}
	if err := c.RecoverFence(ctx, p, b); !errors.Is(err, ErrHold) {
		t.Fatalf("live worker recovered: %v", err)
	}
	s, err := c.Inspect(ctx, p)
	if err != nil || s.State != "REVOKING" {
		t.Fatalf("admission open %+v %v", s, err)
	}
	if err := c.Reserve(ctx, p, strings.Repeat("e", 32)); !errors.Is(err, ErrHold) {
		t.Fatal("closed admission reopened")
	}
	b.stopped = true
	cold := New(db)
	if err := cold.RecoverFence(ctx, p, b); err != nil {
		t.Fatal(err)
	}
	s, err = cold.Inspect(ctx, p)
	if err != nil || s.State != "FENCED" || s.Result != nil {
		t.Fatalf("missing exit invented: %+v %v", s, err)
	}
	if b.stops != 0 || b.observations != 2 {
		t.Fatalf("external stop repeated: %+v", b)
	}
	if err := cold.RecoverFence(ctx, p, b); err != nil {
		t.Fatal(err)
	}
	if b.stops != 0 || b.observations != 2 {
		t.Fatal("completed reconciliation replayed")
	}
	wrong := p
	wrong.Generation++
	if err := cold.RecoverFence(ctx, wrong, b); !errors.Is(err, ErrHold) {
		t.Fatal("old/new generation crossed")
	}
}

func TestRecoverFenceKeepsUnregisteredLaunchUnknown(t *testing.T) {
	ctx := context.Background()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	if err := c.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := c.Reserve(ctx, p, strings.Repeat("d", 32)); err != nil {
		t.Fatal(err)
	}
	b := &recoverFenceBackend{stopped: true}
	if err := c.RecoverFence(ctx, p, b); !errors.Is(err, ErrHold) {
		t.Fatal("unregistered unknown launch retired")
	}
	if b.stops != 0 || b.observations != 0 {
		t.Fatal("missing identity used")
	}
}

func TestRecoverFenceFinishesCrashAfterLaunchFence(t *testing.T) {
	ctx := context.Background()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	id := strings.Repeat("d", 32)
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	for _, err := range []error{c.Create(p), c.Reserve(ctx, p, id), c.Register(ctx, p, id, i), c.Close(ctx, p)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Fault injection: durable launch retirement succeeded; permit completion did not.
	if err := c.change(ctx, p, func(s *Snapshot) error { s.Launches[0].State = "FENCED"; return nil }); err != nil {
		t.Fatal(err)
	}
	b := &recoverFenceBackend{}
	cold := New(db)
	if err := cold.RecoverFence(ctx, p, b); !errors.Is(err, ErrHold) {
		t.Fatalf("stale launch fence retired live/reused incarnation: %v", err)
	}
	partial, err := cold.Inspect(ctx, p)
	if err != nil || partial.State != "REVOKING" {
		t.Fatal("unverified fence completed")
	}
	b.stopped = true
	if err := cold.RecoverFence(ctx, p, b); err != nil {
		t.Fatalf("crash window cannot recover: %v", err)
	}
	if b.observations != 2 {
		t.Fatal("partial recovery skipped fresh stopped identity")
	}
	s, err := cold.Inspect(ctx, p)
	if err != nil || s.State != "FENCED" || s.Result != nil {
		t.Fatalf("completion or missing result changed: %+v %v", s, err)
	}
	if b.stops != 0 {
		t.Fatal("recovery repeated stop")
	}
}

func TestFenceAcceptsRecordedResultAfterSystemdTimeout(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			c, p, _ := administrativeFixture(t)
			ctx := context.Background()
			s, err := c.Inspect(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			l := s.Launches[0]
			result := ExecutionResult{Pin: p, LaunchID: l.ID, Identity: *l.Identity, ExitCode: 0, ObservedAt: time.Now(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}
			if err := c.recordResult(ctx, result); err != nil {
				t.Fatal(err)
			}
			b := &recoverFenceBackend{stopped: stopped}
			err = c.Fence(ctx, p, b)
			if !stopped {
				if !errors.Is(err, ErrHold) {
					t.Fatalf("unverified stop accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("completed result lost after timeout: %v", err)
			}
			s, err = New(c.db).Inspect(ctx, p)
			if err != nil || s.State != "FENCED" || s.Result == nil || s.Result.State != "SUCCEEDED" || s.Launches[0].FenceReason != "stopped by systemd" {
				t.Fatalf("result not accepted: %+v %v", s, err)
			}
		})
	}
}
