package jobs

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/audit"
)

type shutdownTarget struct {
	id        string
	pid       int
	scopeUnit string
	done      <-chan struct{}
}

func (m *Manager) Close(grace time.Duration) (result error) {
	if grace <= 0 {
		grace = 5 * time.Second
	}
	defer func() { result = errors.Join(result, m.drainHeavyRunners(grace+2*time.Second)) }()

	m.mu.Lock()
	m.closing = true
	for id, v := range m.ephemeral {
		wipeReceipt(v)
		delete(m.ephemeral, id)
	}
	targets := make([]shutdownTarget, 0)
	snapshots := make([]Snapshot, 0)
	for _, j := range m.jobs {
		if j.cancelReadOnly != nil {
			j.cancelReadOnly()
			targets = append(targets, shutdownTarget{id: j.ID, done: j.done})
			continue
		}
		if j.External != nil {
			if !isTerminal(j.State) {
				j.State = Unknown
				j.Error = "external runtime detached during shutdown"
				snapshots = append(snapshots, snapshot(j))
			}
			continue
		}
		if j.State != Running && j.State != Queued && j.State != Cancelling {
			continue
		}
		if j.State == Running || j.State == Queued {
			j.State = Cancelling
		}
		if j.Error == "" {
			j.Error = "mcpshka shutting down"
		}
		targets = append(targets, shutdownTarget{id: j.ID, pid: j.PID, scopeUnit: j.ScopeUnit, done: j.done})
		snapshots = append(snapshots, snapshot(j))
	}
	m.mu.Unlock()

	for _, s := range snapshots {
		_ = m.persist(s)
		m.log(audit.Event{
			Type: "job_shutdown_cancel", JobID: s.ID, Command: s.Command, Args: s.Args,
			Workdir: s.Workdir, Result: "termination_requested",
			Details: map[string]any{"command_class": s.CommandClass},
		})
	}
	for _, target := range targets {
		if target.scopeUnit != "" {
			terminateScope(target.scopeUnit, syscall.SIGTERM)
		}
		if target.pid > 0 {
			if err := syscall.Kill(-target.pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
				m.log(audit.Event{
					Type: "job_shutdown_signal_error", JobID: target.id,
					Result: err.Error(),
				})
			}
		}
	}

	deadline := time.Now().Add(grace)
	for _, target := range targets {
		if doneClosed(target.done) {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		timer := time.NewTimer(remaining)
		select {
		case <-target.done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}

	var stuck []shutdownTarget
	for _, target := range targets {
		if !doneClosed(target.done) {
			stuck = append(stuck, target)
			if target.scopeUnit != "" {
				terminateScope(target.scopeUnit, syscall.SIGKILL)
			}
			if target.pid > 0 {
				_ = syscall.Kill(-target.pid, syscall.SIGKILL)
			}
		}
	}
	if len(stuck) == 0 {
		return nil
	}

	killDeadline := time.Now().Add(2 * time.Second)
	for _, target := range stuck {
		if doneClosed(target.done) {
			continue
		}
		remaining := time.Until(killDeadline)
		if remaining <= 0 {
			break
		}
		timer := time.NewTimer(remaining)
		select {
		case <-target.done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}

	var remaining []string
	for _, target := range stuck {
		if !doneClosed(target.done) {
			remaining = append(remaining, target.id)
		}
	}
	if len(remaining) > 0 {
		return fmt.Errorf("jobs did not stop during shutdown: %s", strings.Join(remaining, ","))
	}
	return nil
}

func doneClosed(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}
