package jobs

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"mcpshka/internal/heavyqueue"
)

func cloneQueue(s *heavyqueue.Status) *heavyqueue.Status {
	if s == nil {
		return nil
	}
	copy := *s
	return &copy
}

// A queued gate has a durable PID but cannot execute user code. The running
// budget starts only after admission. The target inherits the ticket's flock
// descriptor, so a supervisor crash cannot silently free a live job's slot.
func (m *Manager) admitHeavyJob(j *Job, t *heavyqueue.Ticket, gate *os.File, budget time.Duration) {
	defer close(j.queueDone)
	defer gate.Close()
	defer t.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-j.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	err := t.Wait(ctx, func(s heavyqueue.Status) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if j.State == Queued {
			j.Queue = cloneQueue(&s)
			_ = m.persist(snapshot(j))
		}
	})
	m.mu.Lock()
	if err == nil && j.State == Queued && !m.closing {
		j.State = Running
		j.StartedAt = time.Now().UTC()
		err = m.persist(snapshot(j))
		if err == nil {
			_, err = gate.Write([]byte{'1'})
		}
		if err == nil {
			go m.watchTimeout(j, budget)
			go m.watchResources(j)
		}
	}
	if err != nil && (j.State == Queued || j.State == Running) {
		j.Error = "heavy queue admission: " + err.Error()
		syscall.Kill(-j.PID, syscall.SIGKILL)
	}
	m.mu.Unlock()
	gate.Close()
	<-j.done
	// The leader's exit alone does not prove descendants released resources.
	for {
		gone, err := unknownGroupGone(j)
		if err == nil && gone {
			return
		}
		time.Sleep(time.Second)
	}
}

// Closing the manager also drains ticket owners of already finished jobs.
// Otherwise a delayed ticket unlink can race state-directory retirement.
func (m *Manager) drainHeavyRunners(budget time.Duration) error {
	m.mu.RLock()
	var pending []<-chan struct{}
	for _, j := range m.jobs {
		if j.queueDone != nil {
			pending = append(pending, j.queueDone)
		}
	}
	m.mu.RUnlock()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for _, done := range pending {
		select {
		case <-done:
		case <-timer.C:
			return errors.New("heavy task queue owners did not drain; resources require reconciliation")
		}
	}
	return nil
}

// UseHostHeavyQueue is wired once by the production service before admission.
// Library/fixture managers otherwise require no host filesystem privileges.
func (m *Manager) UseHostHeavyQueue() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heavyQueue = heavyqueue.Host()
}
