package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"mcpshka/internal/audit"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/policy"
)

// Plan must be read-only. Start is invoked once, only after its binding is durable.
type ExternalRuntime interface {
	Name() string
	Match(command string, args []string, root string) bool
	Plan(id, command string, args []string, root string, opts StartOptions) (json.RawMessage, error)
	Start(binding json.RawMessage) error
	Observe(binding json.RawMessage, cancel bool) (ExternalObservation, error)
}
type ExternalObservation struct {
	Queue             *heavyqueue.Status
	State             State
	ExitCode          *int
	Output            string
	ResourcesReleased bool
	Error             string
	// Unit names the running executor unit, read only for progress.
	Unit string
	// Rebind replaces the job's binding after the runtime restarted a
	// proven no-effect attempt under a fresh identity (State Running).
	Rebind json.RawMessage
}
type ExternalBinding struct {
	Runtime string          `json:"runtime"`
	Binding json.RawMessage `json:"binding"`
	// Cancel is sticky: a cancel that arrives while the sandbox is still being
	// prepared cannot fence anything yet, so every later observation carries
	// it until the job is terminal (08.11: a cancelled full test launched anyway).
	Cancel bool `json:"cancel_requested,omitempty"`
}

func (m *Manager) SetExternalRuntime(r ExternalRuntime) {
	m.mu.Lock()
	m.external = r
	m.mu.Unlock()
	m.ReconcileUnknown(16)
}

func (m *Manager) ExternalRuntimeName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.external == nil {
		return ""
	}
	return m.external.Name()
}

// Caller owns the admission reservation. No backend effect precedes persistence.
func (m *Manager) startExternal(r ExternalRuntime, id, command string, args []string, cwd string, opts StartOptions) (outSnapshot Snapshot, launchErr error) {
	mayHaveStarted := false
	defer func() {
		if launchErr != nil && !mayHaveStarted {
			launchErr = rejectBeforeLaunch(launchErr)
		}
	}()
	reservation := true
	defer func() {
		if reservation {
			m.mu.Lock()
			m.starting--
			m.mu.Unlock()
		}
	}()
	binding, err := r.Plan(id, command, args, cwd, opts)
	if err != nil {
		return Snapshot{}, err
	}
	if !validExternal(&ExternalBinding{Runtime: r.Name(), Binding: binding}) {
		return Snapshot{}, errors.New("invalid external binding")
	}
	dir := filepath.Join(m.root, id)
	if err = os.Mkdir(dir, 0700); err != nil {
		return Snapshot{}, err
	}
	for _, name := range []string{"stdout.log", "stderr.log"} {
		if err = os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			return Snapshot{}, err
		}
	}
	j := &Job{ID: id, Command: command, CommandClass: policy.ClassPrivileged, Args: append([]string(nil), args...), Workdir: cwd, State: Unknown, StartedAt: time.Now().UTC(), External: &ExternalBinding{Runtime: r.Name(), Binding: append(json.RawMessage(nil), binding...)}, stdoutPath: filepath.Join(dir, "stdout.log"), stderrPath: filepath.Join(dir, "stderr.log"), OutputLimitBytes: opts.OutputLimitBytes, TimeoutSeconds: int64(opts.Timeout / time.Second), MemoryLimitBytes: opts.MemoryLimitBytes, CPUTimeLimitSeconds: int64(opts.CPUTimeLimit / time.Second), ProcessLimit: opts.ProcessLimit, CPUQuotaPercent: opts.CPUQuotaPercent}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return Snapshot{}, errors.New("job manager is shutting down")
	}
	if err = m.persist(snapshot(j)); err != nil {
		return Snapshot{}, err
	}
	if m.database == nil {
		for _, path := range []string{filepath.Join(dir, metadataName), dir, m.root} {
			f, e := os.Open(path)
			if e != nil {
				return Snapshot{}, e
			}
			e = f.Sync()
			f.Close()
			if e != nil {
				return Snapshot{}, e
			}
		}
	}
	m.jobs[id] = j
	if opts.ReservedID != "" {
		if err = m.persistLaunch(snapshot(j)); err != nil {
			return snapshot(j), err
		}
	}
	// Registration replaces the admission reservation before the bounded RPC.
	j.externalStarting = true
	m.starting--
	reservation = false
	m.mu.Unlock()
	mayHaveStarted = true
	startErr := r.Start(binding)
	m.mu.Lock()
	j.externalStarting = false
	if m.closing {
		j.State = Unknown
		j.ResourcesReleased = false
		j.Error = "external runtime detached during shutdown"
	} else if j.State == Unknown && !j.ResourcesReleased {
		if startErr != nil {
			j.Error = "external start outcome unknown: " + startErr.Error()
		} else {
			j.State = Running
			j.Error = ""
		}
	}
	if err = m.persist(snapshot(j)); err != nil {
		j.State = Unknown
		j.Error = "external state persistence failed"
		return snapshot(j), err
	}
	m.log(audit.Event{Type: "job_started", JobID: id, Command: command, Args: args, Workdir: cwd, Result: string(j.State)})
	return snapshot(j), nil
}

// Caller holds m.mu. Runtime observations must be bounded by the backend.
func (m *Manager) observeExternal(j *Job, cancel bool) (Snapshot, error) {
	if cancel && !isTerminal(j.State) {
		j.External.Cancel = true
	}
	cancel = j.External.Cancel
	if j.externalStarting {
		return snapshot(j), nil
	}
	if m.external == nil || m.external.Name() != j.External.Runtime {
		return snapshot(j), errors.New("external runtime unavailable or identity mismatch: HOLD")
	}
	// The sticky flag, not "cancel and still running": a re-observed fenced
	// job must keep reading as cancelled, never flip to failed or timed out.
	o, err := m.external.Observe(append(json.RawMessage(nil), j.External.Binding...), cancel)
	before := snapshot(j)
	j.ReconciledAt = time.Now().UTC()
	j.FinishedAt = time.Time{}
	if err != nil {
		j.State = Unknown
		j.ResourcesReleased = false
		j.Error = "external observation unknown: " + err.Error()
	} else {
		j.State = Unknown
		j.ExitCode = nil
		j.ResourcesReleased = false
		j.Error = o.Error
		if isTerminal(o.State) && !o.ResourcesReleased {
			j.Error = "external exit observed; resource fence unverified: HOLD"
		}
		j.Queue = cloneQueue(o.Queue)
		if len(o.Rebind) > 0 && o.State == Running {
			j.External.Binding = append(json.RawMessage(nil), o.Rebind...)
			m.log(audit.Event{Type: "job_external_rebound", JobID: j.ID, Command: j.Command, Workdir: j.Workdir, Result: o.Error})
		}
		if o.State == Running || o.State == Queued || o.State == Cancelling {
			j.State = o.State
		}
		if isTerminal(o.State) && o.ResourcesReleased && o.ExitCode != nil && ((o.State == Done && *o.ExitCode == 0) || (o.State != Done && *o.ExitCode != 0)) {
			j.State = o.State
			j.ExitCode = o.ExitCode
			j.ResourcesReleased = true
			j.FinishedAt = time.Now().UTC()
			if before.State == j.State && !before.FinishedAt.IsZero() {
				j.FinishedAt = before.FinishedAt
			}
		}
		if len(o.Output) > int(j.OutputLimitBytes) {
			o.Output = o.Output[:j.OutputLimitBytes]
		}
		if writeErr := os.WriteFile(j.stdoutPath, []byte(o.Output), 0600); writeErr != nil {
			j.State = Unknown
			j.ResourcesReleased = false
			j.Error = "external output persistence failed"
			err = writeErr
		} else {
			j.OutputStoredBytes = int64(len(o.Output))
		}
	}
	if persistErr := m.persist(snapshot(j)); persistErr != nil {
		j.State = Unknown
		j.ResourcesReleased = false
		j.ExitCode = nil
		j.FinishedAt = before.FinishedAt
		return snapshot(j), persistErr
	}
	if before.State != j.State {
		m.log(audit.Event{Type: "job_external_observed", JobID: j.ID, Command: j.Command, Workdir: j.Workdir, Result: string(j.State), Details: map[string]any{"resources_released": j.ResourcesReleased}})
	}
	out := snapshot(j)
	if err == nil && j.State == Running && o.Unit != "" {
		if obs := observeExecutorUnit(o.Unit); obs.Alive {
			out.Process = &obs
		}
	}
	return out, err
}

func cloneExternal(b *ExternalBinding) *ExternalBinding {
	if b == nil {
		return nil
	}
	return &ExternalBinding{Runtime: b.Runtime, Binding: append(json.RawMessage(nil), b.Binding...), Cancel: b.Cancel}
}

func validExternal(b *ExternalBinding) bool {
	return b != nil && b.Runtime != "" && len(b.Runtime) <= 128 && len(b.Binding) > 0 && len(b.Binding) <= MaxReadLimit && json.Valid(b.Binding)
}

// SealedTestEvidence returns the newest externally sealed job whose binding
// names exactly root and commit, so gates can cite a committed test result.
func (m *Manager) SealedTestEvidence(root, commit string) (Snapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best *Job
	for _, j := range m.jobs {
		if j.External == nil || j.Workdir != root {
			continue
		}
		var b struct {
			Root   string `json:"root"`
			Commit string `json:"commit"`
			Stage  string `json:"stage,omitempty"`
		}
		if json.Unmarshal(j.External.Binding, &b) != nil || b.Root != root || b.Commit != commit || (b.Stage != "" && b.Stage != "test") {
			continue
		}
		if best == nil || j.StartedAt.After(best.StartedAt) {
			best = j
		}
	}
	if best == nil {
		return Snapshot{}, false
	}
	return snapshot(best), true
}

// LastSealedSuccess returns how long the newest passing sealed job of root
// ran, so callers can refuse a budget shorter than a known-good suite.
func (m *Manager) LastSealedSuccess(root string) (time.Duration, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best *Job
	for _, j := range m.jobs {
		if j.External == nil || j.Workdir != root || j.State != Done || j.FinishedAt.IsZero() {
			continue
		}
		var b struct {
			Stage string `json:"stage"`
		}
		if json.Unmarshal(j.External.Binding, &b) != nil || (b.Stage != "" && b.Stage != "test") {
			continue
		}
		if best == nil || j.FinishedAt.After(best.FinishedAt) {
			best = j
		}
	}
	if best == nil {
		return 0, false
	}
	return best.FinishedAt.Sub(best.StartedAt), true
}
