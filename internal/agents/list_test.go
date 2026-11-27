package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// MCP-057: tasks are listed server-side with filters and a cursor.
func TestListTasksPaginatesAndFilters(t *testing.T) {
	old := taskDir
	taskDir = t.TempDir()
	defer func() { taskDir = old }()
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		agent := Codex
		if i%2 == 1 {
			agent = Claude
		}
		if err := saveTask(Task{ID: fmt.Sprintf("%016x", i+1), Agent: agent, Path: "/root/repos/diag", State: "done", StartedAt: t0.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = saveTask(Task{ID: fmt.Sprintf("%016x", 9), Agent: Codex, Path: "/root/repos/other", State: "running", StartedAt: t0})
	_ = os.WriteFile(filepath.Join(taskDir, fmt.Sprintf("%016x", 10)+".json"), []byte("{broken"), 0o600)

	p1, err := ListTasks(ListFilter{Path: "/root/repos/diag/"}, "", 2, time.Time{})
	if err != nil || len(p1.Tasks) != 2 || p1.Tasks[0].ID != fmt.Sprintf("%016x", 5) || p1.NextCursor == "" || p1.Complete || p1.Skipped != 1 {
		t.Fatalf("page1: %+v %v", p1, err)
	}
	p2, _ := ListTasks(ListFilter{Path: "/root/repos/diag"}, p1.NextCursor, 2, time.Time{})
	p3, _ := ListTasks(ListFilter{Path: "/root/repos/diag"}, p2.NextCursor, 2, time.Time{})
	if len(p2.Tasks) != 2 || len(p3.Tasks) != 1 || p3.NextCursor != "" {
		t.Fatalf("pages: %d %d %q", len(p2.Tasks), len(p3.Tasks), p3.NextCursor)
	}
	claude, _ := ListTasks(ListFilter{Agent: Claude}, "", 50, time.Time{})
	if len(claude.Tasks) != 2 {
		t.Fatalf("agent filter: %d", len(claude.Tasks))
	}
	// A reboot after start turns a running record into interrupted, as status does.
	run, _ := ListTasks(ListFilter{State: "interrupted"}, "", 50, t0.Add(time.Minute))
	if len(run.Tasks) != 1 || run.Tasks[0].Path != "/root/repos/other" {
		t.Fatalf("interrupted: %+v", run.Tasks)
	}
	if _, err := ListTasks(ListFilter{}, "../etc", 5, time.Time{}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}
