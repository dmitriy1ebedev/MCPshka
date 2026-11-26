package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"mcpshka/internal/failure"
)

// TaskLeaseCheckpoint contains recovery handles, never the holder credential.
type TaskLeaseCheckpoint struct {
	State      string `json:"state"`
	AcquireKey string `json:"acquire_key"`
	ReleaseKey string `json:"release_key"`
}

type TaskLease interface {
	Acquire(context.Context, string, string, string) (string, error)
	Heartbeat(context.Context, string, string, string) error
	Release(context.Context, string, string, string) error
}

type taskLeaseContextKey struct{}
type taskLeaseContext struct {
	id, path, report string
	save             func(TaskLeaseCheckpoint) error
}
type taskSecretKey struct{}

func redactTaskSecret(ctx context.Context, text string) string {
	if secret, _ := ctx.Value(taskSecretKey{}).(string); secret != "" {
		return strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}

type taskDiagnosticError struct {
	text  string
	cause error
}

func (e *taskDiagnosticError) Error() string { return e.text }
func (e *taskDiagnosticError) Unwrap() error { return e.cause }

// releaseWait bounds how long a finished attempt waits for its own pending
// job (a test it started) before giving the lease back.
var releaseWait, releasePoll = 30 * time.Minute, 20 * time.Second

// releaseWhenIdle: 26.11 two Codex tasks hit their quota while their own
// tests were queued; Release answered PROJECT_BUSY (nothing written) and the
// tasks became UNKNOWN instead of quota-ended. A no-effect refusal is
// repeated with the same key until the job ends or the bound passes.
func releaseWhenIdle(ctx context.Context, lease TaskLease, path, token, key string) error {
	deadline := time.Now().Add(releaseWait)
	for {
		c, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := lease.Release(c, path, token, key)
		cancel()
		if err == nil || !failure.IsNoEffect(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(releasePoll):
		}
	}
}

// heartbeatMisses consecutive transient heartbeat failures stop an attempt.
const heartbeatMisses = 5

// attemptGone proves the attempt unit ended; a variable for tests.
var attemptGone = attemptEnded

// RunWithTaskLease is called only after the root broker rechecks owner consent.
// The root runner holds the credential and lends it to one bounded attempt.
func RunWithTaskLease(ctx context.Context, agent, path, prompt, report string, lease TaskLease, run taskRun) (tail string, resultErr error) {
	task, _ := ctx.Value(taskLeaseContextKey{}).(*taskLeaseContext)
	if task == nil || task.path != path || task.report != report || lease == nil || run == nil {
		return "", failure.Rejected("TASK_LEASE_CONTEXT", "agents.RunWithTaskLease", "authorized task checkpoint and lease authority required", nil)
	}
	checkpoint := TaskLeaseCheckpoint{State: "acquiring", AcquireKey: "agent-task-" + task.id + "-acquire", ReleaseKey: "agent-task-" + task.id + "-release"}
	save := func(state string) error { checkpoint.State = state; return task.save(checkpoint) }
	if err := save("acquiring"); err != nil {
		return "", failure.Rejected("TASK_LEASE_INTENT", "agents.RunWithTaskLease", "task intent could not be saved", err)
	}
	token, err := lease.Acquire(ctx, path, "agent-task:"+task.id, checkpoint.AcquireKey)
	if err != nil || token == "" {
		state := "unknown"
		if failure.IsNoEffect(err) {
			state = "not_acquired"
		}
		saveErr := save(state)
		if state == "not_acquired" && saveErr == nil {
			return "", err
		}
		return "", errors.Join(errAttemptUnknown, err, saveErr)
	}
	privateCtx := context.WithValue(ctx, taskSecretKey{}, token)
	defer func() {
		tail = redactTaskSecret(privateCtx, tail)
		if resultErr != nil {
			resultErr = &taskDiagnosticError{text: redactTaskSecret(privateCtx, resultErr.Error()), cause: resultErr}
		}
	}()
	// Cleanup uses only this task's credential and original key. A live or
	// unverified child retains authority; quota cannot schedule its successor.
	defer func() {
		// MCP-186: an unknown attempt keeps the lease only while its unit may
		// still write; once systemd shows the unit gone, the lease goes back.
		if errors.Is(resultErr, errAttemptUnknown) {
			if gone, err := attemptGone(context.WithoutCancel(ctx), task.id); err != nil || !gone {
				resultErr = errors.Join(resultErr, save("unknown"))
				return
			}
		}
		if err := save("releasing"); err != nil {
			resultErr = errors.Join(errAttemptUnknown, resultErr, err)
			return
		}
		if err := releaseWhenIdle(context.WithoutCancel(ctx), lease, path, token, checkpoint.ReleaseKey); err != nil {
			resultErr = errors.Join(errAttemptUnknown, resultErr, err, save("unknown"))
			return
		}
		if err := save("released"); err != nil {
			resultErr = errors.Join(errAttemptUnknown, resultErr, err)
		}
	}()
	if err := save("held"); err != nil {
		return "", err
	}
	attempt, stopAttempt := context.WithCancelCause(privateCtx)
	defer stopAttempt(nil)
	beat, stopBeat := context.WithCancel(attempt)
	beatDone := make(chan struct{})
	go func() {
		defer close(beatDone)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		misses := 0
		for n := 1; ; n++ {
			select {
			case <-beat.Done():
				return
			case <-ticker.C:
			}
			err := lease.Heartbeat(beat, path, token, fmt.Sprintf("agent-task-%s-beat-%d", task.id, n))
			if beat.Err() != nil {
				return
			}
			if err == nil {
				misses = 0
				continue
			}
			// MCP-288: a missed beat under memory pressure or a busy broker
			// is not a lost lease, which outlives many minutes of silence.
			// Only a verified refusal or a run of misses ends the attempt.
			if misses++; failure.IsNoEffect(err) || misses >= heartbeatMisses {
				stopAttempt(errors.Join(errAttemptUnknown, err))
				return
			}
		}
	}()
	// MCP-186: the prompt is the agent CLI's argv, readable in ps/systemctl;
	// the token goes to a 0600 file next to the report instead.
	tokenFile := strings.TrimSuffix(report, ".report.md") + ".lease-token"
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		stopBeat()
		<-beatDone
		return "", errors.Join(errAttemptUnknown, err)
	}
	defer os.Remove(tokenFile)
	prompt = "Lease проекта уже держит раннер MCPshka: lease_token — в файле " + tokenFile +
		" (прочитай его) — передавай его в инструменты записи и project_test. НЕ вызывай project_lease_acquire, project_lease_resume и project_lease_release; не передавай token в отчёты и журналы.\n\n" + prompt
	tail, resultErr = run(attempt, agent, path, prompt, report)
	stopBeat()
	<-beatDone
	if cause := context.Cause(attempt); errors.Is(cause, errAttemptUnknown) {
		resultErr = errors.Join(resultErr, cause)
	}
	return tail, resultErr
}
