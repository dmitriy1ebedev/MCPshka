package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
)

type taskLeaseFixture struct {
	t                               *testing.T
	id, path                        string
	held                            bool
	acquires, releases, beats       int
	transientBeats                  int // first beats that fail without proving the lease lost
	acquireErr, releaseErr, beatErr error
}

func (l *taskLeaseFixture) Acquire(_ context.Context, path, owner, key string) (string, error) {
	l.acquires++
	task, err := ReadTask(l.id)
	if err != nil || task.Lease == nil || task.Lease.State != "acquiring" || task.Lease.AcquireKey != key || path != l.path || owner != "agent-task:"+l.id {
		l.t.Fatal("effect admitted before its durable intent", task, err)
	}
	if l.acquireErr != nil {
		return "", l.acquireErr
	}
	l.held = true
	return "borrowed-private-token", nil
}
func (l *taskLeaseFixture) Heartbeat(_ context.Context, path, token, key string) error {
	l.beats++
	if path != l.path || token != "borrowed-private-token" || !l.held {
		l.t.Fatal("wrong heartbeat holder")
	}
	if l.transientBeats > 0 {
		l.transientBeats--
		return errors.New("broker busy")
	}
	return l.beatErr
}
func (l *taskLeaseFixture) Release(_ context.Context, path, token, key string) error {
	l.releases++
	task, err := ReadTask(l.id)
	if err != nil || task.Lease.State != "releasing" || task.Lease.ReleaseKey != key || path != l.path || token != "borrowed-private-token" || !l.held {
		l.t.Fatal("cleanup changed holder or lost intent", err)
	}
	if l.releaseErr != nil {
		return l.releaseErr
	}
	l.held = false
	return nil
}
func TestTaskLeaseOwnsAttemptAndCleansOnlyVerifiedExit(t *testing.T) {
	for _, mode := range []string{"ok", "quota", "failed", "unit-unknown", "unit-gone", "release-unknown", "admission-unknown", "admission-refused"} {
		t.Run(mode, func(t *testing.T) {
			oldDir, oldQueue, oldGone := taskDir, taskHeavyQueue, attemptGone
			t.Cleanup(func() { taskDir, taskHeavyQueue, attemptGone = oldDir, oldQueue, oldGone })
			taskDir = t.TempDir()
			taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
			attemptGone = func(context.Context, string) (bool, error) { return mode == "unit-gone", nil }
			id, path := "1234567890abcdef", t.TempDir()
			if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("owner task"), 0600); err != nil {
				t.Fatal(err)
			}
			lease := &taskLeaseFixture{t: t, id: id, path: path}
			if mode == "admission-unknown" {
				lease.acquireErr = errors.New("lost acquire reply")
			}
			if mode == "admission-refused" {
				lease.acquireErr = failure.Rejected("busy", "fixture", "busy", nil)
			}
			if mode == "release-unknown" {
				lease.releaseErr = errors.New("lost release reply")
			}
			calls := 0
			got, err := RunTask(context.Background(), id, Codex, path, func(ctx context.Context, agent, dir, prompt, report string) (string, error) {
				return RunWithTaskLease(ctx, agent, dir, prompt, report, lease, func(_ context.Context, _, _, privatePrompt, _ string) (string, error) {
					calls++
					current, err := ReadTask(id)
					if err != nil || current.Lease.State != "held" || !lease.held || strings.Contains(privatePrompt, "borrowed-private-token") || !leaseFileHolds(privatePrompt, "borrowed-private-token") || !strings.Contains(privatePrompt, "НЕ вызывай project_lease_acquire") {
						t.Fatal("unowned attempt", current, err)
					}
					var runErr error
					switch mode {
					case "quota":
						runErr = &QuotaError{Agent: Codex, Reset: time.Now().Add(time.Hour)}
					case "failed":
						runErr = errors.New("attempt failed borrowed-private-token")
					case "unit-unknown", "unit-gone":
						runErr = errors.Join(&QuotaError{Agent: Codex, Reset: time.Now().Add(time.Hour)}, errAttemptUnknown)
					}
					return "borrowed-private-token tail", runErr
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			want, state, releases := "done", "released", 1
			switch mode {
			case "quota":
				want = "quota"
			case "failed":
				want = "failed"
			case "unit-unknown":
				want, state, releases = "unknown", "unknown", 0
			case "unit-gone": // MCP-186: the unit ended, the lease goes back
				want, state = "unknown", "released"
			case "release-unknown":
				want, state = "unknown", "unknown"
			case "admission-unknown":
				want, state, releases = "unknown", "unknown", 0
			case "admission-refused":
				want, state, releases = "failed", "not_acquired", 0
			}
			if got.State != want || got.Lease == nil || got.Lease.State != state || lease.releases != releases || lease.acquires != 1 || calls != 1 && !strings.HasPrefix(mode, "admission-") || strings.HasPrefix(mode, "admission-") && calls != 0 {
				t.Fatal(got, mode, calls, lease.releases)
			}
			raw, err := os.ReadFile(filepath.Join(taskDir, id+".json"))
			if err != nil || strings.Contains(string(raw), "borrowed-private-token") {
				t.Fatal("credential persisted", err)
			}
		})
	}
}

func TestTaskLeaseHeartbeatStopsAndNeverReleasesUnverifiedAttempt(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "ok", true: "heartbeat-lost"}[lost], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				oldDir, oldQueue, oldGone := taskDir, taskHeavyQueue, attemptGone
				t.Cleanup(func() { taskDir, taskHeavyQueue, attemptGone = oldDir, oldQueue, oldGone })
				attemptGone = func(context.Context, string) (bool, error) { return false, nil }
				taskDir = t.TempDir()
				taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
				const id = "0123456789abcdef"
				path := t.TempDir()
				if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				lease := &taskLeaseFixture{t: t, id: id, path: path}
				if lost {
					lease.beatErr = errors.New("lost heartbeat")
				}
				got, err := RunTask(context.Background(), id, Codex, path, func(ctx context.Context, agent, dir, prompt, report string) (string, error) {
					return RunWithTaskLease(ctx, agent, dir, prompt, report, lease, func(attempt context.Context, _, _, _, _ string) (string, error) {
						if lost {
							<-attempt.Done()
							return "", attempt.Err()
						}
						time.Sleep(2*time.Minute + time.Second)
						return "verified exit", nil
					})
				})
				want, releases := "done", 1
				if lost {
					want, releases = "unknown", 0
				}
				if err != nil || got.State != want || lease.beats < 1 || lease.releases != releases {
					t.Fatal(got, err, lease.beats, lease.releases)
				}
				before := lease.beats
				time.Sleep(2 * time.Minute)
				if lease.beats != before {
					t.Fatal("heartbeat leaked beyond its task")
				}
			})
		})
	}
}

// MCP-288: 26.11 two Codex tasks died 90 s in on one missed heartbeat while
// H96 had 113 MB free; transient misses must not end a live attempt.
func TestTaskLeaseSurvivesTransientHeartbeatMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		oldDir, oldQueue, oldGone := taskDir, taskHeavyQueue, attemptGone
		t.Cleanup(func() { taskDir, taskHeavyQueue, attemptGone = oldDir, oldQueue, oldGone })
		attemptGone = func(context.Context, string) (bool, error) { return true, nil }
		taskDir = t.TempDir()
		taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
		const id = "0123456789abcdee"
		path := t.TempDir()
		if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		lease := &taskLeaseFixture{t: t, id: id, path: path, transientBeats: heartbeatMisses - 1}
		got, err := RunTask(context.Background(), id, Codex, path, func(ctx context.Context, agent, dir, prompt, report string) (string, error) {
			return RunWithTaskLease(ctx, agent, dir, prompt, report, lease, func(attempt context.Context, _, _, _, _ string) (string, error) {
				select {
				case <-attempt.Done():
					return "", attempt.Err()
				case <-time.After(time.Duration(heartbeatMisses+2) * time.Minute):
					return "verified exit", nil
				}
			})
		})
		if err != nil || got.State != "done" || lease.releases != 1 {
			t.Fatal(got, err, lease.beats, lease.releases)
		}
	})
}

func TestTaskLeaseReleasedBeforeQuotaWaitAndSuccessor(t *testing.T) {
	priorDir, priorQueue := taskDir, taskHeavyQueue
	t.Cleanup(func() { taskDir, taskHeavyQueue = priorDir, priorQueue })
	taskDir = t.TempDir()
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	const id = "abcdef0123456789"
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	lease := &taskLeaseFixture{t: t, path: path}
	now := time.Now()
	calls := 0
	run := func(ctx context.Context, agent, dir, prompt, report string) (string, error) {
		lease.id = strings.TrimSuffix(filepath.Base(report), ".report.md")
		return RunWithTaskLease(ctx, agent, dir, prompt, report, lease, func(context.Context, string, string, string, string) (string, error) {
			calls++
			if calls == 1 {
				return "usage limit", &QuotaError{Agent: Codex, Reset: now.Add(time.Hour)}
			}
			return "verified exit", nil
		})
	}
	got, err := runTaskChain(context.Background(), id, Codex, path, run, func() time.Time { return now }, func(_ context.Context, d time.Duration) error {
		parent, err := ReadTask(id)
		if err != nil || lease.held || parent.Lease.State != "released" {
			t.Fatal("quota wait stranded holder", parent, err)
		}
		now = now.Add(d)
		return nil
	}, nil)
	if err != nil || got.State != "done" || calls != 2 || lease.acquires != 2 || lease.releases != 2 || lease.held {
		t.Fatal(got, err, calls, lease)
	}
}

func TestTaskResumeCannotSkipOriginalLeaseReconciliation(t *testing.T) {
	for _, state := range []string{"acquiring", "held", "releasing", "unknown", "released", "not_acquired"} {
		task := Task{ID: "0123456789abcdef", State: "unknown", Lease: &TaskLeaseCheckpoint{State: state, AcquireKey: "original-acquire", ReleaseKey: "original-release"}}
		err := checkResume(task, func() (bool, error) { return true, nil })
		safe := state == "released" || state == "not_acquired"
		if (err == nil) != safe {
			t.Fatal("ended unit bypassed original lease cleanup", state, err)
		}
	}
}

// leaseFileHolds: the prompt names the 0600 token file instead of the token.
func leaseFileHolds(prompt, token string) bool {
	i := strings.Index(prompt, "в файле ")
	if i < 0 {
		return false
	}
	path := strings.Fields(prompt[i+len("в файле "):])[0]
	raw, err := os.ReadFile(path)
	fi, serr := os.Stat(path)
	return err == nil && serr == nil && fi.Mode().Perm() == 0o600 && strings.TrimSpace(string(raw)) == token
}
