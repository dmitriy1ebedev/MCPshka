package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type taskRun func(context.Context, string, string, string, string) (string, error)

// RunTaskRunner keeps an authorized task alive across quota and transport failures.
// The unit's nine-hour bound leaves room for shutdown after this eight-hour budget.
func RunTaskRunner(ctx context.Context, id, agent, path string, run taskRun) (Task, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Hour)
	defer cancel()
	return runTaskChain(ctx, id, agent, path, run, time.Now, func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}, agentNetworkReady)
}

func runTaskChain(ctx context.Context, id, agent, path string, run taskRun, now func() time.Time, sleep func(context.Context, time.Duration) error, networkReady func(context.Context, string) error) (result Task, resultErr error) {
	if !ValidTaskID(id) {
		return Task{}, errors.New("invalid task id")
	}
	// One runner owns the whole chain. Existing records are observed, never replayed.
	lock, err := os.OpenFile(filepath.Join(taskDir, id+".runner.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return Task{}, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return Task{}, errors.New("task runner busy; observe agent_task_status")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	defer func() {
		if resultErr == nil {
			return
		}
		t, err := ReadTask(id)
		if err == nil && (t.State == "quota" || t.State == "retry_scheduled") && !t.ResumeAt.IsZero() && t.ResumedBy == "" {
			t.ResumeAt = time.Time{}
			t.NextAction = "runner stopped; reconcile task/lease/jobs before manual resume_of=" + t.ID
			if err = saveTask(t); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()
	original, err := ReadPrompt(id)
	if err != nil {
		return Task{}, err
	}
	resumeOf := ""
	transportRetries := 0
	for resumes := 0; resumes <= 3; resumes++ {
		t, readErr := ReadTask(id)
		if os.IsNotExist(readErr) {
			t, err = runTask(ctx, id, agent, path, resumeOf, true, run)
			if err != nil {
				return t, err
			}
		} else if readErr != nil {
			return t, readErr
		}
		if t.Agent != agent || t.Path != path {
			return t, errors.New("task identity changed; reconcile before resume")
		}
		if t.ResumedBy != "" {
			resumeOf, id = t.ID, t.ResumedBy
			continue
		}
		if t.State != "quota" && t.State != "retry_scheduled" {
			return t, nil
		}
		at := t.ResetAt.Add(time.Minute)
		transport := t.State == "retry_scheduled"
		if transport {
			at = now().Add(transportDelay(transportRetries))
			transportRetries++
		} else if t.ResetAt.IsZero() || !t.ResetAt.After(now()) {
			t.ResumeAt = time.Time{}
			t.NextAction = "quota reset unknown or stale; obtain provider evidence before manual resume_of=" + t.ID
			return t, saveTask(t)
		}
		for {
			if deadline, ok := ctx.Deadline(); resumes == 3 || (ok && !at.Before(deadline)) {
				t.ResumeAt = time.Time{}
				t.NextAction = "retry exceeds runner budget; reconcile before manual resume_of=" + t.ID
				return t, saveTask(t)
			}
			t.ResumeAt = at
			t.NextAction = "runner will automatically resume_of=" + t.ID + " after " + at.Format(time.RFC3339) + "; observe agent_task_status"
			if err = saveTask(t); err != nil {
				return t, err
			}
			// RunTask has closed its ticket; waiting never occupies the host queue.
			if err = sleep(ctx, at.Sub(now())); err != nil {
				t.ResumeAt = time.Time{}
				t.NextAction = "retry wait interrupted; reconcile lease/jobs, then agent_task resume_of=" + t.ID
				return t, errors.Join(err, saveTask(t))
			}
			if err = ctx.Err(); err != nil {
				return t, err
			}
			if networkReady == nil {
				break
			}
			if err = networkReady(ctx, agent); err == nil {
				break
			}
			t.State, t.Error = "retry_scheduled", err.Error()
			at = now().Add(transportDelay(transportRetries))
			transportRetries++
		}

		prompt, err := ResumePrompt(t, original, "")
		if err != nil {
			return t, err
		}
		sum := sha256.Sum256([]byte(t.ID + ":quota-resume-v1"))
		child := hex.EncodeToString(sum[:8])
		f, err := os.OpenFile(filepath.Join(taskDir, child+".prompt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0640)
		if err != nil {
			return t, fmt.Errorf("successor preparation UNKNOWN; inspect %s before retry: %w", child, err)
		}
		if os.Geteuid() == 0 {
			if err := f.Chown(994, 984); err != nil { // mcpshka:mcpshka, 994 is the sgx group
				f.Close()
				return t, err
			}
		}
		_, writeErr := f.WriteString(prompt)
		closeErr := f.Close()
		if writeErr != nil {
			return t, writeErr
		}
		if closeErr != nil {
			return t, closeErr
		}
		t.ResumeAt = time.Time{}
		t.ResumedBy = child
		t.NextAction = "observe agent_task_status id=" + child
		if err = saveTask(t); err != nil {
			return t, err
		}
		resumeOf, id = t.ID, child
	}
	return Task{}, errors.New("resume chain limit reached; reconcile agent_task_status")
}

func transportDelay(attempt int) time.Duration {
	return []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}[min(attempt, 2)]
}

// HTTP errors such as 401/403 prove connectivity without requiring credentials.
func agentNetworkReady(ctx context.Context, agent string) error {
	endpoint := "https://chatgpt.com/"
	if agent == Claude {
		endpoint = "https://api.anthropic.com/"
	} else if agent != Codex {
		return nil
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probe, http.MethodHead, endpoint, nil)
	if err != nil {
		return err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("executor network preflight: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("executor network preflight: HTTP %d", resp.StatusCode)
	}
	return nil
}
