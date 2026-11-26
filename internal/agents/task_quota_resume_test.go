package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/heavyqueue"
)

func TestTaskRunnerResumesQuotaWithoutHoldingQueue(t *testing.T) {
	oldDir, oldQueue := taskDir, taskHeavyQueue
	t.Cleanup(func() { taskDir, taskHeavyQueue = oldDir, oldQueue })
	taskDir = t.TempDir()
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	id := "0123456789abcdef"
	os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("finish M1"), 0600)
	now := time.Now()
	calls, sleeps := 0, 0
	run := func(ctx context.Context, agent, dir, prompt, report string) (string, error) {
		calls++
		if calls == 1 {
			return "WIP", &QuotaError{Agent: agent, Reset: now.Add(time.Hour)}
		}
		if !strings.Contains(prompt, "finish M1") || !strings.Contains(prompt, "git diff") || !strings.Contains(prompt, id) || !strings.Contains(prompt, "квот") {
			t.Fatal(prompt)
		}
		return "TASK_DONE", nil
	}
	sleep := func(ctx context.Context, d time.Duration) error {
		sleeps++
		parent, err := ReadTask(id)
		if err != nil || parent.State != "quota" || parent.ResumeAt.IsZero() {
			t.Fatal(parent, err)
		}
		entries, _ := os.ReadDir(taskHeavyQueue.Dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".ticket") {
				t.Fatal("quota wait occupies heavy queue")
			}
		}
		now = now.Add(d)
		return nil
	}
	got, err := runTaskChain(context.Background(), id, Codex, "/root/repos/example", run, func() time.Time { return now }, sleep, nil)
	parent, _ := ReadTask(id)
	if err != nil || got.State != "done" || got.ResumeOf != id || parent.ResumedBy != got.ID || calls != 2 || sleeps != 1 {
		t.Fatal(got, parent, err, calls, sleeps)
	}
	// A replay observes the existing successor; it must never invoke the agent again.
	again, err := runTaskChain(context.Background(), id, Codex, "/root/repos/example", run, func() time.Time { return now }, sleep, nil)
	if err != nil || again.ID != got.ID || calls != 2 {
		t.Fatal(again, err, calls)
	}
}

func TestTaskRunnerUnknownResetDoesNotResume(t *testing.T) {
	oldDir, oldQueue := taskDir, taskHeavyQueue
	t.Cleanup(func() { taskDir, taskHeavyQueue = oldDir, oldQueue })
	taskDir = t.TempDir()
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	id := "0011223344556677"
	os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("task"), 0600)
	run := func(context.Context, string, string, string, string) (string, error) {
		return "", &QuotaError{Agent: Codex}
	}
	got, err := runTaskChain(context.Background(), id, Codex, "/root/repos/example", run, time.Now, func(context.Context, time.Duration) error { t.Fatal("unknown reset slept"); return nil }, nil)
	if err != nil || got.State != "quota" || got.ResumedBy != "" || !strings.Contains(got.NextAction, "unknown") {
		t.Fatal(got, err)
	}
}

func TestPendingQuotaWaitInterruptedByBoot(t *testing.T) {
	boot := time.Now()
	task := Task{ID: "0123456789abcdef", State: "quota", StartedAt: boot.Add(-time.Hour), ResumeAt: boot.Add(time.Hour)}
	got := MarkInterrupted(task, boot)
	if got.State != "interrupted" || !strings.Contains(got.NextAction, "resume_of=") {
		t.Fatal(got)
	}
	task.ResumeAt = time.Time{}
	if MarkInterrupted(task, boot).State != "quota" {
		t.Fatal("unscheduled quota changed")
	}
}

func TestQuotaAttemptUnitLifetimeAndBounds(t *testing.T) {
	args, err := attemptUnitArgs("0123456789abcdef", "0011223344556677", "/root/repos/example", []string{"codex", "exec", "task"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, bound := range []string{"Slice=mcpshka-heavy.slice", "Nice=10", "IOSchedulingClass=idle", "--unit=mcpshka-agent-task-0123456789abcdef", "--wait --pipe", "BindsTo=mcpshka-agent-task-runner-0011223344556677.service", "KillMode=control-group", "RuntimeMaxSec=10800s", "MemoryMax=1G", "MemoryHigh=512M", "MemorySwapMax=256M", "TasksMax=512", "CPUQuota=150%", "-- codex exec task"} {
		if !strings.Contains(joined, bound) {
			t.Fatal("missing bound", bound, joined)
		}
	}
	// P31.6: Claude Code gets its own, larger envelope.
	claude, _ := attemptUnitArgs("0123456789abcdef", "0011223344556677", "/p", []string{"/root/.local/bin/claude", "-p"})
	if !strings.Contains(strings.Join(claude, " "), "MemoryMax=1536M") {
		t.Fatal("claude limits", claude)
	}
	if _, err := attemptUnitArgs("../bad", "0011223344556677", "/p", []string{"codex"}); err == nil {
		t.Fatal("invalid unit accepted")
	}
}

func TestTaskRunnerStopsAtBudgetStaleResetAndCancellation(t *testing.T) {
	for _, mode := range []string{"budget", "stale", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			oldDir, oldQueue := taskDir, taskHeavyQueue
			t.Cleanup(func() { taskDir, taskHeavyQueue = oldDir, oldQueue })
			taskDir = t.TempDir()
			taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
			id := "1234567890abcdef"
			os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("finish"), 0600)
			now := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			calls := 0
			run := func(context.Context, string, string, string, string) (string, error) {
				calls++
				reset := now.Add(time.Hour)
				if mode == "stale" {
					reset = now.Add(-time.Minute)
				}
				if mode == "cancel" {
					reset = now.Add(time.Second)
				}
				return "", &QuotaError{Agent: Codex, Reset: reset}
			}
			got, err := runTaskChain(ctx, id, Codex, "/root/repos/example", run, func() time.Time { return now }, func(context.Context, time.Duration) error {
				if mode != "cancel" {
					t.Fatal("must not wait", mode)
				}
				return context.Canceled
			}, nil)
			if calls != 1 || got.ResumedBy != "" || !got.ResumeAt.IsZero() || got.NextAction == "" {
				t.Fatal(got, err, calls)
			}
			if mode == "cancel" && err == nil {
				t.Fatal("cancellation lost")
			}
			saved, _ := ReadTask(id)
			if !saved.ResumeAt.IsZero() {
				t.Fatal("dead runner still promises a resume", saved)
			}
		})
	}
}

func TestTaskRunnerNeverReexecutesAnObservedAttempt(t *testing.T) {
	previous := taskDir
	taskDir = t.TempDir()
	t.Cleanup(func() { taskDir = previous })
	id := "1234567890abcdef"
	os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("finish"), 0600)
	for _, state := range []string{"running", "QUEUED", "done", "failed", "unknown"} {
		saveTask(Task{ID: id, Agent: Codex, Path: "/root/repos/example", State: state})
		got, err := runTaskChain(context.Background(), id, Codex, "/root/repos/example", func(context.Context, string, string, string, string) (string, error) {
			t.Fatal("observed attempt replayed")
			return "", nil
		}, time.Now, func(context.Context, time.Duration) error { t.Fatal("unexpected sleep"); return nil }, nil)
		if err != nil || got.State != state {
			t.Fatal(got, err)
		}
	}
}

func TestUnknownAttemptNeverSchedulesQuotaResume(t *testing.T) {
	previousDir, previousQueue := taskDir, taskHeavyQueue
	taskDir = t.TempDir()
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	t.Cleanup(func() { taskDir, taskHeavyQueue = previousDir, previousQueue })
	id := "1234567890abcdef"
	os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("finish"), 0600)
	got, err := runTaskChain(context.Background(), id, Codex, "/root/repos/example", func(context.Context, string, string, string, string) (string, error) {
		return "usage limit", errAttemptUnknown
	}, time.Now, func(context.Context, time.Duration) error { t.Fatal("unknown outcome retried"); return nil }, nil)
	if err != nil || got.State != "unknown" || !got.ResumeAt.IsZero() || got.NextAction == "" {
		t.Fatal(got, err)
	}
}

func TestManualResumeProbesUnknownAndRejectsScheduledTasks(t *testing.T) {
	for _, tc := range []struct {
		state                                       string
		ended, unverified, pending, successor, want bool
	}{
		{state: "unknown", ended: true, want: true},
		{state: "unknown"}, {state: "unknown", ended: true, unverified: true},
		{state: "quota", want: true}, {state: "quota", pending: true},
		{state: "failed", want: true}, {state: "interrupted", want: true},
		{state: "running"}, {state: "QUEUED"}, {state: "done"},
		{state: "quota", successor: true},
	} {
		task := Task{ID: "1234567890abcdef", State: tc.state}
		if tc.pending {
			task.ResumeAt = time.Now().Add(time.Hour)
		}
		if tc.successor {
			task.ResumedBy = "0011223344556677"
		}
		called := 0
		err := checkResume(task, func() (bool, error) {
			called++
			if tc.unverified {
				return false, context.DeadlineExceeded
			}
			return tc.ended, nil
		})
		if (err == nil) != tc.want || (tc.state != "unknown" && called != 0) {
			t.Fatal(tc, err, called)
		}
	}
}

func TestTaskRunnerUnknownCleanupTakesPrecedenceOverQuota(t *testing.T) {
	oldDir, oldQueue := taskDir, taskHeavyQueue
	t.Cleanup(func() { taskDir, taskHeavyQueue = oldDir, oldQueue })
	taskDir = t.TempDir()
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	id := "abcdef0123456789"
	if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	got, err := runTaskChain(context.Background(), id, Codex, t.TempDir(), func(context.Context, string, string, string, string) (string, error) {
		calls++
		return "usage limit", errors.Join(&QuotaError{Agent: Codex, Reset: time.Now().Add(time.Hour)}, errAttemptUnknown)
	}, time.Now, func(context.Context, time.Duration) error {
		t.Fatal("unknown lease cleanup scheduled a quota successor")
		return nil
	}, nil)
	if err != nil || got.State != "unknown" || calls != 1 || got.ResumedBy != "" || !got.ResumeAt.IsZero() {
		t.Fatal(got, err, calls)
	}
}
