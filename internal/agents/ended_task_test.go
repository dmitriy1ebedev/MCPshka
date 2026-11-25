package agents

import (
	"testing"
	"time"
)

func TestEndedTaskCovering(t *testing.T) {
	taskDir = t.TempDir()
	defer func() { taskDir = TaskDir }()
	start := time.Date(2026, 10, 23, 7, 50, 0, 0, time.UTC)
	acquired, last := start.Add(2*time.Minute), start.Add(5*time.Minute)
	if err := saveTask(Task{ID: "c0c6ec1e655ed872", Agent: Codex, Path: "/root/repos/diag", State: "failed", StartedAt: start, FinishedAt: start.Add(6 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := EndedTaskCovering("/root/repos/diag", acquired, last); !ok {
		t.Fatal("ended task not found")
	}
	if _, ok := EndedTaskCovering("/root/repos/diag", acquired, start.Add(7*time.Minute)); ok {
		t.Fatal("heartbeat after the task ended belongs to someone else")
	}
	if _, ok := EndedTaskCovering("/root/repos/other", acquired, last); ok {
		t.Fatal("other path matched")
	}
	// The resumed run started after the lease was taken waits for it; it is
	// not the holder (BUG-20261024-91).
	if err := saveTask(Task{ID: "b0c6ec1e655ed872", Agent: Codex, Path: "/root/repos/diag", State: "running", StartedAt: start.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := EndedTaskCovering("/root/repos/diag", acquired, last); !ok {
		t.Fatal("successor task blocked the ended holder")
	}
	if err := saveTask(Task{ID: "a0c6ec1e655ed872", Agent: Codex, Path: "/root/repos/diag", State: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if _, ok := EndedTaskCovering("/root/repos/diag", acquired, last); ok {
		t.Fatal("a running task on the path may hold the lease")
	}
}
