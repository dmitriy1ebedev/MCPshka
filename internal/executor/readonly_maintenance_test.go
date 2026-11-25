package executor

import (
	"context"
	"errors"
	"mcpshka/internal/failure"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadonlyMaintenanceDoesNotCloseLiveWorker(t *testing.T) {
	ctx := context.Background()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	id := strings.Repeat("d", 32)
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	for _, err := range []error{c.Create(p), c.Reserve(ctx, p, id), c.Register(ctx, p, id, i)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	m := Manifest{Purpose: "project-test-v1", Pin: p, Runs: map[string]RunSpec{id: {Profile: "project-test-v1"}}}
	b := &recoverFenceBackend{}
	lockCalls := 0
	lock := func(ctx context.Context, p Pin, fn func() error) error { lockCalls++; return fn() }
	err = c.maintainReadonlyPermit(ctx, m, b, lock)
	if !errors.Is(err, ErrHold) {
		t.Fatalf("live worker accepted: %v", err)
	}
	if reason := failure.Details(err); reason == nil || reason.Class != failure.FencedUnknown || reason.Code != "WORKER_STOP_UNVERIFIED" {
		t.Fatalf("unproven stop classified as known busy owner: %v", err)
	}
	s, err := c.Inspect(ctx, p)
	if err != nil || s.State != "ACTIVE" || lockCalls != 0 || b.stops != 0 {
		t.Fatalf("live writer modified: %+v %v", s, err)
	}
	b.stopped = true
	if err := c.maintainReadonlyPermit(ctx, m, b, lock); err != nil {
		t.Fatal(err)
	}
	s, err = c.Inspect(ctx, p)
	if err != nil || s.State != "FENCED" || s.Result != nil || lockCalls != 1 || b.stops != 0 {
		t.Fatalf("cold recovery invented result: %+v %v", s, err)
	}
	before := b.observations
	if err := New(db).maintainReadonlyPermit(ctx, m, b, lock); err != nil {
		t.Fatal(err)
	}
	if b.observations != before || lockCalls != 1 {
		t.Fatal("completed sweep replayed")
	}
}

func TestReadonlyMaintenanceRejectsForeignPurposeAndLaunch(t *testing.T) {
	c := New(nil)
	b := &recoverFenceBackend{stopped: true}
	for _, m := range []Manifest{{Purpose: "integration-v1"}, {Purpose: "project-test-v1", Runs: map[string]RunSpec{}}} {
		if err := c.maintainReadonlyPermit(context.Background(), m, b, nil); !errors.Is(err, ErrHold) {
			t.Fatal("unsealed or foreign runtime accepted")
		}
	}
	if b.observations != 0 || b.stops != 0 {
		t.Fatal("foreign identity observed")
	}
}

func TestReadonlyMaintenanceFencesRecordedResultExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		lost bool
		want string
	}{
		{"success", 0, false, "SUCCEEDED"}, {"failed", 1, false, "FAILED"}, {"lost_stop_reply", 0, true, "SUCCEEDED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			c := New(db)
			p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
			id := strings.Repeat("d", 32)
			identity := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
			for _, err := range []error{c.Create(p), c.Reserve(ctx, p, id), c.Register(ctx, p, id, identity)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := c.recordResult(ctx, ExecutionResult{Pin: p, LaunchID: id, Identity: identity, ExitCode: tc.code, ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}); err != nil {
				t.Fatal(err)
			}
			b := &exactResultFence{t: t, launch: id, identity: identity, lost: tc.lost}
			lock := func(_ context.Context, pin Pin, fn func() error) error {
				if pin != p {
					t.Fatal("wrong permit")
				}
				return fn()
			}
			m := Manifest{Purpose: ModuleTestPurpose, Pin: p, Runs: map[string]RunSpec{id: {}}}
			wrong := m
			wrong.Runs = map[string]RunSpec{strings.Repeat("e", 32): {}}
			if err := c.maintainReadonlyPermit(ctx, wrong, b, lock); !errors.Is(err, ErrHold) {
				t.Fatalf("foreign launch: %v", err)
			}
			if b.calls != 0 {
				t.Fatal("foreign launch stopped")
			}
			if tc.lost {
				if err := c.maintainReadonlyPermit(ctx, m, b, lock); err != nil {
					t.Fatalf("verified stopped worker after lost stop reply: %v", err)
				}
				observed, err := c.Inspect(ctx, p)
				if err != nil || observed.State != "FENCED" || observed.Launches[0].FenceReason != "stopped by systemd" {
					t.Fatalf("stop observation not recorded: %+v %v", observed, err)
				}
			}
			for range 2 {
				if err := New(db).maintainReadonlyPermit(ctx, m, b, lock); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Fence(ctx, p, b); err != nil {
				t.Fatalf("manual fence after automatic fence: %v", err)
			}
			s, err := c.Inspect(ctx, p)
			if err != nil || s.NextAction != "" || s.State != "FENCED" || s.Result.State != tc.want || b.calls != 1 {
				t.Fatalf("fence outcome: %+v calls=%d err=%v", s, b.calls, err)
			}
		})
	}
}

type exactResultFence struct {
	t        *testing.T
	launch   string
	identity Identity
	calls    int
	lost     bool
}

func (b *exactResultFence) StopAndVerify(_ context.Context, id string, identity Identity) error {
	if id != b.launch || identity != b.identity {
		b.t.Fatal("wrong incarnation")
	}
	b.calls++
	if b.lost {
		return ErrHold
	}
	return nil
}

func (b *exactResultFence) VerifyStopped(_ context.Context, id string, identity Identity) error {
	if id != b.launch || identity != b.identity {
		b.t.Fatal("wrong observed incarnation")
	}
	if b.calls == 0 {
		return ErrHold
	}
	return nil
}
