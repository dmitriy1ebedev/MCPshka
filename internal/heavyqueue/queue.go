// Package heavyqueue provides a host-wide FIFO for expensive work. Kernel file
// locks, rather than stale timestamps, determine which tickets are still alive.
package heavyqueue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/hostload"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const Directory = "/var/lib/mcpshka/heavy-queue"

type Status struct {
	Scope              string    `json:"scope,omitempty"`
	Parent             string    `json:"parent,omitempty"`
	State              string    `json:"state"`
	Reason             string    `json:"reason,omitempty"`
	Position           int       `json:"position,omitempty"`
	Label              string    `json:"label"`
	RunningAt          time.Time `json:"running_at,omitempty"`
	QueuedAt           time.Time `json:"queued_at,omitempty"`
	WaitSeconds        int64     `json:"wait_seconds,omitempty"`
	AheadCount         int       `json:"ahead_count,omitempty"`
	HeadLabel          string    `json:"head_label,omitempty"`
	HeadRunningSeconds int64     `json:"head_running_seconds,omitempty"`
}
type Queue struct {
	Dir   string
	Poll  time.Duration
	Ready func() (bool, string)
}

func Host() Queue { return Queue{Dir: Directory, Poll: 5 * time.Second, Ready: HostReady} }
func HostReady() (bool, string) {
	if r := hostload.Read(); r.Busy {
		return false, "host busy: " + r.String()
	}
	return resourcesReady(time.Now())
}

type Ticket struct {
	q      Queue
	File   *os.File
	name   string
	status Status
}

func (q Queue) lock() (*os.File, error)         { return q.lockDirectory(true) }
func (q Queue) lockExisting() (*os.File, error) { return q.lockDirectory(false) }
func (q Queue) lockDirectory(create bool) (*os.File, error) {
	if q.Dir == "" {
		return nil, errors.New("queue directory required")
	}
	if create {
		if err := os.MkdirAll(q.Dir, 0770); err != nil {
			return nil, err
		}
	}
	st, err := os.Lstat(q.Dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe queue directory")
	}
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(filepath.Join(q.Dir, ".lock"), flags, 0660)
	if err != nil {
		return nil, err
	}
	if err = queueFileMode(f); err != nil {
		f.Close()
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func unlock(f *os.File)                               { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
func (q Queue) Enqueue(label string) (*Ticket, error) { return q.EnqueueScoped(label, "") }

// A delegated test is a continuation of the active agent's reservation for
// the same leased repository. Its children serialize inside that reservation;
// unrelated queued work cannot deadlock the agent waiting for its test.
func (q Queue) EnqueueScoped(label, scope string) (*Ticket, error) {
	if label == "" || len(label) > 256 {
		return nil, errors.New("invalid queue label")
	}
	lock, err := q.lock()
	if err != nil {
		return nil, err
	}
	defer unlock(lock)
	var random [12]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%020d-%s.ticket", time.Now().UnixNano(), hex.EncodeToString(random[:]))
	f, err := os.OpenFile(filepath.Join(q.Dir, name), os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0660)
	if err != nil {
		return nil, err
	}
	if err = queueFileMode(f); err != nil {
		f.Close()
		os.Remove(filepath.Join(q.Dir, name))
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	t := &Ticket{q: q, File: f, name: name, status: Status{State: "QUEUED", Reason: "waiting for admission", Label: label, Scope: scope, QueuedAt: time.Now().UTC()}}
	if err = t.save(); err != nil {
		os.Remove(filepath.Join(q.Dir, t.name))
		t.File.Close()
		return nil, err
	}
	return t, nil
}
func (t *Ticket) save() error {
	raw, err := json.Marshal(t.status)
	if err != nil {
		return err
	}
	if err = t.File.Truncate(0); err != nil {
		return err
	}
	_, err = t.File.WriteAt(raw, 0)
	return err
}

// live must be called under .lock. An unlocked ticket belongs to a dead
// supervisor (and no inherited child); it can no longer occupy the queue.
func (q Queue) live(self string) ([]Status, []string, error) {
	entries, err := os.ReadDir(q.Dir)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) > 4096 {
		return nil, nil, errors.New("heavy queue entry budget exceeded")
	}
	states, names := []Status{}, []string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".ticket") {
			continue
		}
		f, err := os.OpenFile(filepath.Join(q.Dir, e.Name()), os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil, nil, err
		}
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() > 4096 {
			f.Close()
			return nil, nil, errors.New("invalid queue ticket")
		}
		if e.Name() != self {
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				os.Remove(filepath.Join(q.Dir, e.Name()))
				f.Close()
				continue
			}
			if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
				f.Close()
				return nil, nil, err
			}
		}
		raw := make([]byte, st.Size())
		_, err = f.ReadAt(raw, 0)
		f.Close()
		if err != nil {
			return nil, nil, err
		}
		var s Status
		if json.Unmarshal(raw, &s) != nil {
			return nil, nil, errors.New("invalid queue record")
		}
		states = append(states, s)
		names = append(names, e.Name())
	}
	return states, names, nil
}
func (t *Ticket) inspect(admit bool) (Status, error) {
	// Read caller readiness before taking the host lock. A jobs callback may
	// need the manager mutex while a status RPC observes this queue.
	check := t.q.Ready
	if check == nil {
		check = HostReady
	}
	hostReady, hostReason := check()
	lock, err := t.q.lockExisting()
	if err != nil {
		return t.status, err
	}
	defer unlock(lock)
	states, names, err := t.q.live(t.name)
	if err != nil {
		return t.status, err
	}
	s := t.status
	if s.State == "RUNNING" {
		return s, nil
	}

	parent := ""
	if s.Scope != "" && !strings.HasPrefix(s.Label, "agent:") {
		for _, x := range states {
			if x.State == "RUNNING" && x.Scope == s.Scope && strings.HasPrefix(x.Label, "agent:") {
				parent = x.Label
				break
			}
		}
	}
	s.Parent = parent
	// An admitted agent blocks other heavy work only while one of its own
	// heavy children runs; an agent that is thinking held the slot for hours
	// and timed out a DIAG test and release builds (BUG-20261024-97). Agent
	// compiles outside the queue still raise load and fail host readiness.
	busyAgent := map[string]bool{}
	for _, x := range states {
		if x.State == "RUNNING" && x.Parent != "" {
			busyAgent[x.Parent] = true
		}
	}
	blocks := func(x Status) bool {
		if x.State != "RUNNING" || x.Label == parent {
			return false
		}
		return !strings.HasPrefix(x.Label, "agent:") || busyAgent[x.Label]
	}
	position := 0
	running := false
	found := false
	for i, n := range names {
		x := states[i]
		if x.State == "RUNNING" {
			if blocks(x) {
				running = true
			}
			continue
		}
		if parent != "" && (x.Scope != s.Scope || strings.HasPrefix(x.Label, "agent:")) {
			continue
		}
		position++
		if n == t.name {
			found = true
			break
		}
	}
	// Running work behind a queued continuation also excludes admission.
	for _, x := range states {
		if blocks(x) {
			running = true
		}
	}
	if !found {
		return s, errors.New("queue ticket disappeared")
	}
	// Every status poll must recompute blockers from live tickets, not append
	// onto the previous poll's counters or stale head label.
	s.AheadCount, s.HeadLabel, s.HeadRunningSeconds = 0, "", 0
	// Give callers actionable, bounded evidence about the head of the FIFO.
	// Only running work or tickets earlier in the queue block admission;
	// an idle agent already admitted to the queue is not a blocker.
	for i, n := range names {
		if n == t.name {
			continue
		}
		x := states[i]
		if x.State == "RUNNING" {
			if !blocks(x) {
				continue
			}
		} else {
			if n > t.name || (parent != "" && (x.Scope != s.Scope || strings.HasPrefix(x.Label, "agent:"))) {
				continue
			}
		}
		s.AheadCount++
		if s.HeadLabel == "" {
			s.HeadLabel = x.Label
			if x.State == "RUNNING" && !x.RunningAt.IsZero() {
				s.HeadRunningSeconds = int64(time.Since(x.RunningAt).Seconds())
			}
		}
	}
	if !s.QueuedAt.IsZero() {
		s.WaitSeconds = int64(time.Since(s.QueuedAt).Seconds())
	}
	s.Position = position
	s.Reason = "another heavy task is running or ahead in FIFO"
	ready := false
	if !running && position == 1 {
		ready, s.Reason = hostReady, hostReason
	}
	if admit && ready {
		s.State, s.Reason, s.Position = "RUNNING", "", 0
		s.RunningAt = time.Now().UTC()
	}
	t.status = s
	if err = t.save(); err != nil {
		return s, err
	}
	return s, nil
}
func (t *Ticket) Status() (Status, error) { return t.inspect(false) }

// Run enqueues label, waits at most limit for admission, then runs fn while
// holding the slot, so fn never overlaps another heavy task on the host.
func (q Queue) Run(label string, limit time.Duration, fn func() error) error {
	ticket, err := q.Enqueue(label)
	if err != nil {
		return fmt.Errorf("heavy queue: %w", err)
	}
	defer ticket.Close()
	wait, cancel := context.WithTimeout(context.Background(), limit)
	err = ticket.Wait(wait, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("heavy queue: %w", err)
	}
	return fn()
}
func (t *Ticket) Wait(ctx context.Context, changed func(Status)) error {
	poll := t.q.Poll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		s, err := t.inspect(true)
		if err != nil {
			return err
		}
		if changed != nil {
			changed(s)
		}
		if s.State == "RUNNING" {
			return nil
		}
		timer.Reset(poll)
	}
}
func (t *Ticket) Close() error {
	if t.File == nil {
		return nil
	}
	lock, err := t.q.lockExisting()
	if err != nil {
		closeErr := t.File.Close()
		t.File = nil
		if os.IsNotExist(err) {
			return closeErr
		}
		return err
	}
	defer unlock(lock)
	// Keep the inode locked until after unlink: no stale collector can race
	// deletion with a live ticket of the same name.
	err = os.Remove(filepath.Join(t.q.Dir, t.name))
	closeErr := t.File.Close()
	t.File = nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return closeErr
}
func (q Queue) Lookup(label string) (*Status, error) {
	if _, err := os.Stat(q.Dir); os.IsNotExist(err) {
		return nil, nil
	}
	lock, err := q.lock()
	if err != nil {
		return nil, err
	}
	defer unlock(lock)
	states, _, err := q.live("")
	if err != nil {
		return nil, err
	}
	for _, s := range states {
		if s.Label == label {
			return &s, nil
		}
	}
	return nil, nil
}

func queueFileMode(f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	raw, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || raw.Nlink != 1 {
		return errors.New("invalid queue lock file")
	}
	if st.Mode().Perm() != 0660 {
		return f.Chmod(0660)
	}
	return nil
}
