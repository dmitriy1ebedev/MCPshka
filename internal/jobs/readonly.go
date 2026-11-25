package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"mcpshka/internal/llmgateway"
)

const llmOutputLimit = 1 << 20

// StartLLM keeps prompts in memory and returns a normal, bounded job receipt.
// Only the gateway callback may run here; this is not an interpreter launcher.
// AsyncLLM is code_run's hook: it starts run as an LLM job and hands back
// the job with the next step; a nil manager refuses instead of panicking.
func (m *Manager) AsyncLLM(ctx context.Context, run func(context.Context) (any, error)) (any, error) {
	if m == nil {
		return nil, errors.New("LLM job manager unavailable")
	}
	job, err := m.StartLLM(ctx, run)
	return map[string]any{"job": job, "next": "job_wait, then job_output"}, err
}

func (m *Manager) StartLLM(parent context.Context, run func(context.Context) (any, error)) (Snapshot, error) {
	return m.startNative(parent, "mcpshka-llm", 600, run)
}

// StartFixes only orchestrates existing gated tools; checks still use project_test.
// StartCode runs one async code_run script as a native job (S5.CM.5): it
// keeps going after the chat that started it is closed.
func (m *Manager) StartCode(parent context.Context, seconds int, run func(context.Context) (any, error)) (Snapshot, error) {
	return m.startNative(parent, "mcpshka-code", seconds, run)
}

func (m *Manager) StartFixes(parent context.Context, run func(context.Context) (any, error)) (Snapshot, error) {
	return m.startNative(parent, "mcpshka-fixes", 3600, run)
}

func (m *Manager) startNative(parent context.Context, command string, seconds int, run func(context.Context) (any, error)) (Snapshot, error) {
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.Command == command && !isTerminal(j.State) {
			m.mu.Unlock()
			return Snapshot{}, errors.New("BUSY: LLM job already running")
		}
	}
	// Reserve capacity across receipt creation as well.
	if m.llmStarting {
		m.mu.Unlock()
		return Snapshot{}, errors.New("BUSY: LLM job starting")
	}
	m.llmStarting = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.llmStarting = false; m.mu.Unlock() }()
	if len(m.policy.AllowedWorkdirs) == 0 {
		return Snapshot{}, errors.New("LLM job requires an allowed workdir")
	}
	s, e := m.StoreReceipt(command, nil, m.policy.AllowedWorkdirs[0], "")
	if e != nil {
		return s, e
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Duration(seconds)*time.Second)
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		cancel()
		return Snapshot{}, errors.New("job manager closing")
	}
	j := m.jobs[s.ID]
	j.State = Running
	j.FinishedAt = time.Time{}
	j.TimeoutSeconds = int64(seconds)
	j.OutputLimitBytes = llmOutputLimit
	j.done = make(chan struct{})
	j.cancelReadOnly = cancel
	s = snapshot(j)
	if e = m.persist(s); e != nil {
		j.State = Failed
		close(j.done)
		m.mu.Unlock()
		cancel()
		return Snapshot{}, e
	}
	m.mu.Unlock()
	go func() {
		defer cancel()
		var value any
		var err error
		func() {
			defer func() {
				if recover() != nil {
					err = errors.New("LLM worker failed")
				}
			}()
			value, err = run(ctx)
		}()
		raw, marshalErr := json.Marshal(value)
		if err == nil {
			err = marshalErr
		}
		if len(raw) > llmOutputLimit {
			err = errors.New("LLM result exceeds output limit")
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		j.State = Done
		code := 0
		if err != nil {
			j.State = Failed
			code = 1
			// The real reason (redacted), not a guess: NIGHT_NOT_QUIET read as a
			// gateway failure on 18.11 (MCP-231).
			raw, _ = json.Marshal(map[string]string{"error": llmgateway.Redact(err.Error()).Text})
		}
		if ctx.Err() != nil {
			j.State = Cancelled
			code = 1
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				j.State = TimedOut
			}
		}
		if e := os.WriteFile(j.stdoutPath, raw, 0600); e != nil {
			j.State = Failed
			code = 1
			j.Error = "cannot save LLM result"
		}
		j.ExitCode = &code
		j.OutputStoredBytes = int64(len(raw))
		j.FinishedAt = time.Now().UTC()
		j.ResourcesReleased = true
		j.cancelReadOnly = nil
		if e := m.persist(snapshot(j)); e != nil {
			j.Error = "cannot persist LLM completion"
		}
		close(j.done)
	}()
	return s, nil
}
