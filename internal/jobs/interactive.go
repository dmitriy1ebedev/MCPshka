package jobs

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"mcpshka/internal/audit"
	"os"
	"os/exec"
	"sort"
	"syscall"
	"time"
)

const MaxInteractiveSessions = 8
const InteractiveIdleTTL = 10 * time.Minute

// prepareInteractive shares job launch, command policy, quotas and cancellation.
func prepareInteractive(cmd *exec.Cmd, pty bool) (input, terminal *os.File, err error) {
	if !pty {
		var reader *os.File
		reader, input, err = os.Pipe()
		if err == nil {
			cmd.Stdin = reader
		}
		return
	}
	fd, e := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if e != nil {
		err = e
		return
	}
	terminal = os.NewFile(uintptr(fd), "pty")
	fail := func(e error) (*os.File, *os.File, error) { terminal.Close(); return nil, nil, e }
	if e = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); e != nil {
		return fail(e)
	}
	n, e := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if e != nil {
		return fail(e)
	}
	slave, e := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if e != nil {
		return fail(e)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr.Setpgid = false
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0
	return terminal, terminal, nil
}
func (m *Manager) AuthorizeInteractive(id, owner string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j := m.jobs[id]
	if j != nil && j.SessionOwner != "" && j.SessionOwner != owner {
		return errors.New("process session belongs to another principal")
	}
	return nil
}
func (m *Manager) InteractiveSessions(owner string) []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Snapshot{}
	for _, j := range m.jobs {
		if j.SessionOwner == owner {
			out = append(out, snapshot(j))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
func (m *Manager) TouchInteractive(id, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	if j == nil || j.SessionOwner == "" || j.SessionOwner != owner {
		return errors.New("process session not found")
	}
	j.lastInteraction = time.Now()
	return nil
}
func (m *Manager) SendInput(id, owner, text string) error {
	if len(text) > MaxReadLimit {
		return errors.New("stdin exceeds 64 KiB")
	}
	if err := m.TouchInteractive(id, owner); err != nil {
		return err
	}
	m.mu.RLock()
	j := m.jobs[id]
	input := j.input
	running := j.State == Running
	m.mu.RUnlock()
	if input == nil || !running {
		return errors.New("process stdin is closed")
	}
	j.inputMu.Lock()
	defer j.inputMu.Unlock()
	if err := input.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	n, err := io.WriteString(input, text)
	m.log(audit.Event{Type: "proc_send", JobID: id, Result: fmt.Sprint(err), Details: map[string]any{"bytes": n}})
	return err
}
func (m *Manager) watchInteractive(j *Job, ttl time.Duration) {
	tick := time.NewTicker(min(ttl, time.Second))
	defer tick.Stop()
	for {
		select {
		case <-j.done:
			return
		case <-tick.C:
			m.mu.RLock()
			last := j.lastInteraction
			m.mu.RUnlock()
			if time.Since(last) >= ttl {
				_, _ = m.Cancel(j.ID)
				return
			}
		}
	}
}
func (j *Job) finishInteractive() {
	if j.input != nil {
		j.input.Close()
	}
	if j.terminalDone != nil {
		select {
		case <-j.terminalDone:
		case <-time.After(100 * time.Millisecond):
		}
		j.terminal.Close()
		<-j.terminalDone
	}
}
