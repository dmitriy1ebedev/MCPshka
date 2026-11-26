package jobs

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"mcpshka/internal/audit"
	"mcpshka/internal/budget"
)

// Budget is the flexible budget a start of this command would get; callers
// show its explanation (budget.Decision.String) to the AI.
func (m *Manager) Budget(command string, args []string, workdir string, requested time.Duration) budget.Decision {
	return budget.Decide(budget.Input{Requested: requested, Default: m.defaultTimeout, History: m.successDurations(command, args, workdir), Load: m.load(), CPUs: runtime.NumCPU()})
}

// progressWindow is how recent progress must be for a job at its budget to be
// extended instead of ended.
const progressWindow = 5 * time.Minute

func (m *Manager) watchTimeout(j *Job, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-j.done:
			timer.Stop()
			return
		case <-timer.C:
		}

		m.mu.Lock()
		if j.State != Running && j.State != Queued {
			m.mu.Unlock()
			return
		}
		now := time.Now()
		if add := budget.Extension(j.StartedAt, deadline, now); j.SessionOwner == "" && add > 0 && !j.lastActive.IsZero() && now.Sub(j.lastActive) < progressWindow {
			deadline = now.Add(add)
			j.TimeoutSeconds = int64(deadline.Sub(j.StartedAt) / time.Second)
			if j.CPUTimeLimitSeconds > 0 && j.CPUQuotaPercent > 0 {
				j.CPUTimeLimitSeconds = min(j.CPUTimeLimitSeconds+int64(add/time.Second)*int64(j.CPUQuotaPercent)/100, int64(MaxCPUTimeLimit/time.Second))
			}
			s := snapshot(j)
			m.mu.Unlock()
			_ = m.persist(s)
			m.log(audit.Event{
				Type: "job_extended", JobID: s.ID, Command: s.Command, Args: s.Args, Workdir: s.Workdir,
				Result: "extended", Details: map[string]any{"added_seconds": int64(add / time.Second), "timeout_seconds": s.TimeoutSeconds},
			})
			continue
		}
		j.timedOut = true
		j.State = Cancelling
		s := snapshot(j)
		pid := j.PID
		scopeUnit := j.ScopeUnit
		m.mu.Unlock()

		_ = m.persist(s)
		m.log(audit.Event{
			Type: "job_timeout", JobID: s.ID, Command: s.Command, Args: s.Args,
			Workdir: s.Workdir, Result: "termination_requested",
		})
		m.terminateJob(pid, scopeUnit, j.done)
		return
	}
}

// noteProgress records CPU, disk writes or output as progress and reports a
// stall when none was seen for the job's stall window.
func (m *Manager) noteProgress(j *Job, obs ProcessObservation, now time.Time) string {
	if j.SessionOwner != "" {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if j.observed && (obs.CPUTicks > j.lastTicks || obs.WriteBytes > j.lastWrite) {
		j.lastActive = now
	}
	j.observed = true
	j.lastTicks, j.lastWrite = max(j.lastTicks, obs.CPUTicks), max(j.lastWrite, obs.WriteBytes)
	out := j.LastOutputAt
	if j.quota != nil {
		out = j.quota.lastActivity()
	}
	if out.After(j.lastActive) {
		j.lastActive = out
	}
	quietFrom := j.StartedAt
	if j.lastActive.After(quietFrom) {
		quietFrom = j.lastActive
	}
	if j.idle > 0 && j.State == Running && !quietFrom.IsZero() && now.Sub(quietFrom) > j.idle {
		return fmt.Sprintf("stalled: no output, CPU or disk writes for %s (stall window of the flexible budget)", j.idle.Round(time.Second))
	}
	return ""
}

func (m *Manager) load() float64 {
	if m.loadAvg == nil {
		return 0
	}
	return m.loadAvg()
}

func loadAverage() float64 { return budget.HostLoad() }

// successDurations are the wall times of recent successful runs of the same
// command, arguments and workdir, oldest first.
func (m *Manager) successDurations(command string, args []string, workdir string) []time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	type run struct {
		at time.Time
		d  time.Duration
	}
	var runs []run
	for _, j := range m.jobs {
		if j.State != Done || j.FinishedAt.IsZero() || j.StartedAt.IsZero() || j.Command != command || j.Workdir != workdir || strings.Join(j.Args, "\x00") != strings.Join(args, "\x00") {
			continue
		}
		runs = append(runs, run{j.FinishedAt, j.FinishedAt.Sub(j.StartedAt)})
	}
	sort.Slice(runs, func(a, b int) bool { return runs[a].at.Before(runs[b].at) })
	out := make([]time.Duration, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.d)
	}
	return out
}

func (m *Manager) watchResources(j *Job) {
	ticker := time.NewTicker(ResourcePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-j.done:
			return
		case <-ticker.C:
		}

		obs := m.observeJob(j)
		if !obs.Alive {
			continue
		}
		m.memoryPause(j, time.Now())
		m.mu.RLock()
		violation := resourceViolation(j, obs, m.clockTicksPerSecond)
		m.mu.RUnlock()
		if stall := m.noteProgress(j, obs, time.Now()); violation == "" {
			violation = stall
		}
		if violation == "" {
			continue
		}

		m.mu.Lock()
		if j.State != Running && j.State != Queued {
			m.mu.Unlock()
			return
		}
		j.ResourceViolation = violation
		j.State = Cancelling
		s := snapshot(j)
		pid := j.PID
		scopeUnit := j.ScopeUnit
		m.mu.Unlock()

		_ = m.persist(s)
		m.log(audit.Event{
			Type: "job_resource_limit", JobID: s.ID, Command: s.Command, Args: s.Args,
			Workdir: s.Workdir, Result: "termination_requested",
			Details: map[string]any{"violation": violation},
		})
		m.terminateJob(pid, scopeUnit, j.done)
		return
	}
}

func (m *Manager) observeJob(j *Job) ProcessObservation {
	if j.ScopeUnit != "" {
		if obs := observeScope(j.ScopeUnit); obs.Alive {
			return obs
		}
	}
	return observeProcessGroup(j.PID)
}

func resourceViolation(j *Job, obs ProcessObservation, clockTicksPerSecond uint64) string {
	switch {
	case j.MemoryLimitBytes > 0 && obs.RSSBytes > uint64(j.MemoryLimitBytes):
		return fmt.Sprintf("memory rss %d > %d bytes", obs.RSSBytes, j.MemoryLimitBytes)
	case j.ProcessLimit > 0 && obs.ProcessCount > j.ProcessLimit:
		return fmt.Sprintf("process count %d > %d", obs.ProcessCount, j.ProcessLimit)
	case j.CPUTimeLimitSeconds > 0 && clockTicksPerSecond > 0 &&
		obs.CPUTicks > uint64(j.CPUTimeLimitSeconds)*clockTicksPerSecond:
		return fmt.Sprintf("cpu time ticks %d > %d", obs.CPUTicks, uint64(j.CPUTimeLimitSeconds)*clockTicksPerSecond)
	default:
		return ""
	}
}

func (m *Manager) wait(j *Job, stdout, stderr *os.File) {
	err := j.cmd.Wait()
	j.finishInteractive()
	_ = stdout.Close()
	_ = stderr.Close()

	m.mu.Lock()
	j.FinishedAt = time.Now().UTC()
	switch {
	case j.timedOut:
		j.State = TimedOut
		if err != nil {
			j.Error = "job exceeded wall-clock timeout"
		}
	case j.ResourceViolation != "":
		j.State = Failed
		j.Error = "job exceeded resource limit: " + j.ResourceViolation
	case j.State == Cancelling:
		j.State = Cancelled
	case err == nil:
		j.State = Done
	case strings.HasPrefix(j.Error, "heavy queue admission: "):
		// The queue killed the gate; its reason must survive the exit status,
		// or a refused admission reads as a bare "signal: killed".
		j.State = Failed
	default:
		j.State = Failed
		j.Error = err.Error()
	}
	if ps := j.cmd.ProcessState; ps != nil {
		code := ps.ExitCode()
		j.ExitCode = &code
	}
	s := snapshot(j)
	_ = m.persist(s)
	close(j.done)
	m.mu.Unlock()

	m.log(audit.Event{
		Type: "job_finished", JobID: s.ID, Command: s.Command, Args: s.Args,
		Workdir: s.Workdir, Result: string(s.State),
		Duration: s.FinishedAt.Sub(s.StartedAt).Milliseconds(),
		Details: map[string]any{
			"output_stored_bytes":  s.OutputStoredBytes,
			"output_dropped_bytes": s.OutputDroppedBytes,
		},
	})
}
