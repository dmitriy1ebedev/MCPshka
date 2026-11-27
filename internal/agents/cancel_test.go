package agents

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fakeUnits(stopErr error, ended map[string]bool) (unitControl, *[]string) {
	var stopped []string
	return unitControl{
		stop: func(_ context.Context, unit string) error {
			stopped = append(stopped, unit)
			if stopErr == nil {
				ended[unit] = true
			}
			return stopErr
		},
		ended: func(_ context.Context, unit string) (bool, error) { return ended[unit], nil },
	}, &stopped
}

func TestCancelRecordsOnlyAfterBothUnitsEnded(t *testing.T) {
	taskDir = t.TempDir()
	t.Cleanup(func() { taskDir = TaskDir })
	now := time.Now().UTC()
	task := Task{ID: "1d2d642b261e0879", Agent: Codex, Path: "/root/repos/lane", State: "unknown", StartedAt: now.Add(-time.Hour)}
	u, stopped := fakeUnits(nil, map[string]bool{})
	got, changed, err := cancelTask(context.Background(), task, u, now)
	if err != nil || !changed || got.State != "cancelled" || got.NextAction == "" || len(*stopped) != 2 {
		t.Fatalf("cancel: %+v changed=%v err=%v stopped=%v", got, changed, err, *stopped)
	}
	if err := checkResume(got, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal("cancelled task must allow explicit continuation:", err)
	}
}

func TestCancelLeavesRecordWhenStopUnknown(t *testing.T) {
	taskDir = t.TempDir()
	t.Cleanup(func() { taskDir = TaskDir })
	task := Task{ID: "aaaaaaaaaaaaaaaa", State: "running"}
	u, _ := fakeUnits(errors.New("dbus timeout"), map[string]bool{})
	got, changed, err := cancelTask(context.Background(), task, u, time.Now())
	if err == nil || changed || got.State != "running" {
		t.Fatalf("unknown stop must not record cancel: %+v %v %v", got, changed, err)
	}
}

func TestCancelFinishedTaskIsNoop(t *testing.T) {
	u, stopped := fakeUnits(nil, map[string]bool{})
	got, changed, err := cancelTask(context.Background(), Task{ID: "bbbbbbbbbbbbbbbb", State: "done"}, u, time.Now())
	if err != nil || changed || got.State != "done" || len(*stopped) != 0 {
		t.Fatalf("done task touched: %+v %v %v", got, changed, err)
	}
}

// A task killed mid-run never recorded its lease release; once the lease
// store no longer holds it for the task, a continuation is allowed.
func TestResumeAfterLeaseFreedByStore(t *testing.T) {
	task := Task{ID: "cccccccccccccccc", Path: "/root/repos/x", State: "cancelled", Lease: &TaskLeaseCheckpoint{State: "unknown"}}
	if err := CheckResume(context.Background(), task, func(string, string) bool { return true }); err == nil {
		t.Fatal("a held lease must block the successor")
	}
	if err := CheckResume(context.Background(), task, func(path, owner string) bool { return owner != "agent-task:"+task.ID }); err != nil {
		t.Fatalf("freed lease: %v", err)
	}
}
