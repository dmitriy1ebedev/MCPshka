package agents

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/heavyqueue"
)

func TestTaskRunnerTransportRecovery(t *testing.T) {
	for _, diagnostic := range []string{"failed to refresh available models: request timed out", "workspace routing discovery timed out", "error sending request for url (https://chatgpt.com/backend-api/codex/responses): dns error", "task assertion failed"} {
		t.Run(diagnostic, func(t *testing.T) {
			oldDir, oldQueue := taskDir, taskHeavyQueue
			t.Cleanup(func() { taskDir, taskHeavyQueue = oldDir, oldQueue })
			taskDir = t.TempDir()
			taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
			id := "0123456789abcdef"
			if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("finish task"), 0600); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(t.TempDir(), "codex")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\necho '"+diagnostic+"' >&2\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", filepath.Dir(binary)+":"+os.Getenv("PATH"))
			now := time.Now()
			calls, waits := 0, 0
			run := func(ctx context.Context, agent, path, prompt, report string) (string, error) {
				calls++
				if calls == 1 {
					return Run(ctx, agent, path, prompt, report)
				}
				if !strings.Contains(prompt, "git diff") {
					t.Fatal(prompt)
				}
				return "TASK_DONE", nil
			}
			got, err := runTaskChain(context.Background(), id, Codex, t.TempDir(), run, func() time.Time { return now }, func(_ context.Context, d time.Duration) error {
				waits++
				saved, e := ReadTask(id)
				if e != nil || saved.State != "retry_scheduled" || saved.ResumeAt.IsZero() {
					t.Fatal(saved, e)
				}
				if d != transportDelay(waits-1) {
					t.Fatal(d)
				}
				now = now.Add(d)
				return nil
			}, func(context.Context, string) error {
				if waits == 1 {
					return errors.New("DNS unavailable")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if diagnostic == "task assertion failed" {
				if got.State != "failed" || calls != 1 || waits != 0 {
					t.Fatal(got, calls, waits)
				}
			} else if got.State != "done" || calls != 2 || waits != 2 {
				t.Fatal(got, calls, waits)
			}
		})
	}
}

func TestTaskRunnerTransportBudgetAndUnknown(t *testing.T) {
	for _, mode := range []string{"limit", "deadline", "unknown", "cancel", "offline"} {
		t.Run(mode, func(t *testing.T) {
			oldDir, oldQueue := taskDir, taskHeavyQueue
			t.Cleanup(func() { taskDir, taskHeavyQueue = oldDir, oldQueue })
			taskDir = t.TempDir()
			taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
			id := "0011223344556677"
			if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("task"), 0600); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Hour)
			defer cancel()
			if mode == "deadline" {
				now = now.Add(8 * time.Hour)
			}
			calls, waits := 0, 0
			got, err := runTaskChain(ctx, id, Codex, t.TempDir(), func(context.Context, string, string, string, string) (string, error) {
				calls++
				transport := &TransportError{Agent: Codex, Cause: errors.New("timeout")}
				if mode == "unknown" {
					return "", errors.Join(transport, errAttemptUnknown)
				}
				return "", transport
			}, func() time.Time { return now }, func(_ context.Context, d time.Duration) error {
				if d != transportDelay(waits) {
					t.Fatal(d, waits)
				}
				waits++
				if mode == "cancel" {
					return context.Canceled
				}
				now = now.Add(d)
				return nil
			}, func(context.Context, string) error {
				if mode == "offline" {
					return errors.New("DNS unavailable")
				}
				return nil
			})
			if mode == "limit" {
				if err != nil || calls != 4 || waits != 3 || got.State != "retry_scheduled" || !got.ResumeAt.IsZero() {
					t.Fatal(got, err, calls, waits)
				}
			} else if calls != 1 || got.ResumedBy != "" || (mode != "cancel" && mode != "offline" && waits != 0) {
				t.Fatal(got, err, calls, waits)
			}
			if mode == "offline" && (waits < 3 || got.State != "retry_scheduled" || now.Sub(time.Now()) > 8*time.Hour) {
				t.Fatal(got, waits, now)
			}
			if mode == "unknown" && got.State != "unknown" {
				t.Fatal(got)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			saved, e := ReadTask(id)
			if e != nil || !saved.ResumeAt.IsZero() {
				t.Fatal(saved, e)
			}
		})
	}
}

func TestTransportResumeLifecycle(t *testing.T) {
	now := time.Now()
	task := Task{ID: "0011223344556677", State: "retry_scheduled", StartedAt: now.Add(-time.Hour), ResumeAt: now.Add(time.Minute)}
	if got := MarkInterrupted(task, now); got.State != "interrupted" {
		t.Fatal(got)
	}
	if checkResume(task, nil) == nil {
		t.Fatal("scheduled transport retry admitted a second writer")
	}
	task.ResumeAt = time.Time{}
	if err := checkResume(task, nil); err != nil {
		t.Fatal(err)
	}
}

type transportProbeFunc func(*http.Request) (*http.Response, error)

func (f transportProbeFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAgentNetworkPreflight(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, status := range []int{401, 403, 503} {
		http.DefaultTransport = transportProbeFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodHead || r.URL.Host != "chatgpt.com" {
				t.Fatal(r)
			}
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 10*time.Second {
				t.Fatal("unbounded probe")
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		if err := agentNetworkReady(context.Background(), Codex); (err != nil) != (status >= 500) {
			t.Fatal(status, err)
		}
	}
	http.DefaultTransport = transportProbeFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "timeout", Name: "chatgpt.com", IsTimeout: true}
	})
	if err := agentNetworkReady(context.Background(), Codex); err == nil {
		t.Fatal("DNS outage admitted")
	}
}

func TestTaskRunnerQuotaThenTransport(t *testing.T) {
	oldDir, oldQueue := taskDir, taskHeavyQueue
	t.Cleanup(func() { taskDir, taskHeavyQueue = oldDir, oldQueue })
	taskDir = t.TempDir()
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	id := "0123456789abcdef"
	if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	calls, waits := 0, 0
	got, err := runTaskChain(context.Background(), id, Codex, t.TempDir(), func(context.Context, string, string, string, string) (string, error) {
		calls++
		switch calls {
		case 1:
			return "", &QuotaError{Agent: Codex, Reset: now.Add(time.Hour)}
		case 2:
			return "", &TransportError{Agent: Codex, Cause: errors.New("workspace routing discovery timed out")}
		default:
			return "TASK_DONE", nil
		}
	}, func() time.Time { return now }, func(_ context.Context, d time.Duration) error {
		expected := time.Hour + time.Minute
		if waits > 0 {
			expected = time.Minute
		}
		if d != expected {
			t.Fatal(d, expected)
		}
		waits++
		now = now.Add(d)
		return nil
	}, func(context.Context, string) error { return nil })
	if err != nil || got.State != "done" || calls != 3 || waits != 2 {
		t.Fatal(got, err, calls, waits)
	}
}
