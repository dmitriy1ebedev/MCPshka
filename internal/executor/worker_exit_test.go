package executor

import (
	"context"
	"errors"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerExitExportSurvivesRunCancellation(t *testing.T) {
	ctx := context.Background()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	id := strings.Repeat("d", 32)
	identity := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	for _, e := range []error{c.Create(p), c.Reserve(ctx, p, id), c.Register(ctx, p, id, identity)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	run, cancel := context.WithCancel(ctx)
	cancel()
	result := ExecutionResult{Pin: p, LaunchID: id, Identity: identity, ExitCode: -1, WaitFailed: true, Output: "terminated on run deadline", ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}
	if err := persistWorkerExit(run, func(export context.Context) error { return c.recordResult(export, result) }); err != nil {
		t.Fatalf("cancelled run lost exit evidence: %v", err)
	}
	cold := New(db)
	s, err := cold.Inspect(ctx, p)
	if err != nil || s.Result == nil || s.Result.ExitCode != -1 || !s.Result.WaitFailed || s.Result.State != "EXITED_UNFENCED" {
		t.Fatalf("cold export %+v %v", s, err)
	}
	if err := cold.Fence(ctx, p, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	s, err = cold.Inspect(ctx, p)
	if err != nil || s.Result.State != "FAILED" {
		t.Fatalf("deadline promoted to success: %+v %v", s, err)
	}
}

func TestWorkerExitExportHasFiniteIndependentDeadline(t *testing.T) {
	run, cancel := context.WithCancel(context.Background())
	cancel()
	sentinel := errors.New("authority write failed")
	start := time.Now()
	err := persistWorkerExit(run, func(export context.Context) error {
		if export.Err() != nil {
			t.Fatal("execution cancellation propagated into final evidence write")
		}
		deadline, ok := export.Deadline()
		if !ok || deadline.Sub(start) > 3100*time.Millisecond {
			t.Fatal("unbounded final evidence write")
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("authority error hidden: %v", err)
	}
}
