package jobs

import (
	"fmt"
	"syscall"
	"time"

	"mcpshka/internal/audit"
	"mcpshka/internal/heavyqueue"
)

// P31.7: when the host runs out of memory mid-run (a stream plus a compile
// left 40 MB free and load 19 on 13.11), a running heavy job is frozen with
// SIGSTOP instead of killed, and resumed when memory returns. A pause is
// bounded: after maxPause it resumes anyway and is not frozen again for
// pauseCooldown, so a job never hangs for good. While frozen it counts as
// active: no stall kill, and the wall budget is extended as for progress.
var (
	hostMemory    = heavyqueue.Memory
	signalGroup   = func(pgid int, sig syscall.Signal) error { return syscall.Kill(-pgid, sig) }
	maxPause      = 5 * time.Minute
	pauseCooldown = 2 * time.Minute
	pauseCheckGap = 5 * time.Second
)

// memoryPause runs on each resource tick of a heavy (queued) job.
func (m *Manager) memoryPause(j *Job, now time.Time) {
	m.mu.Lock()
	if j.Queue == nil || j.State != Running || now.Sub(j.pauseChecked) < pauseCheckGap {
		if j.Paused != "" {
			j.lastActive = now
		}
		m.mu.Unlock()
		return
	}
	j.pauseChecked = now
	mem := hostMemory()
	var event, result string
	switch {
	case j.Paused == "" && mem.Available >= 0 && mem.Available < heavyqueue.PauseBelow && now.After(j.pauseCooldown):
		if signalGroup(j.PID, syscall.SIGSTOP) != nil {
			break
		}
		j.Paused = fmt.Sprintf("paused: low memory, %s; resumes above %d MiB or after %s", mem, heavyqueue.ResumeAbove>>20, maxPause)
		j.pausedAt = now
		event, result = "job_paused", j.Paused
	case j.Paused != "" && (mem.Available < 0 || mem.Available > heavyqueue.ResumeAbove || now.Sub(j.pausedAt) >= maxPause):
		_ = signalGroup(j.PID, syscall.SIGCONT)
		j.PausedSeconds += int64(now.Sub(j.pausedAt) / time.Second)
		if now.Sub(j.pausedAt) >= maxPause {
			j.pauseCooldown = now.Add(pauseCooldown)
		}
		j.Paused = ""
		event, result = "job_resumed", mem.String()
	}
	if j.Paused != "" {
		j.lastActive = now
	}
	if event == "" {
		m.mu.Unlock()
		return
	}
	s := snapshot(j)
	m.mu.Unlock()
	_ = m.persist(s)
	m.log(audit.Event{Type: event, JobID: s.ID, Command: s.Command, Args: s.Args, Workdir: s.Workdir, Result: result})
}
