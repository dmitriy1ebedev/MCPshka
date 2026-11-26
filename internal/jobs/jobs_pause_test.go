package jobs

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"mcpshka/internal/heavyqueue"
)

// A heavy job is frozen, not killed, while memory is low, shows why in its
// status, and resumes when memory returns or the pause bound runs out.
func TestMemoryPauseFreezesAndResumesHeavyJob(t *testing.T) {
	m, _ := testManager(t)
	priorMem, priorSig := hostMemory, signalGroup
	defer func() { hostMemory, signalGroup = priorMem, priorSig }()
	avail := int64(100 << 20)
	hostMemory = func() heavyqueue.Mem { return heavyqueue.Mem{Available: avail, SwapTotal: 100, SwapFree: 1} }
	var sent []syscall.Signal
	signalGroup = func(_ int, sig syscall.Signal) error { sent = append(sent, sig); return nil }

	j := &Job{ID: "heavy", PID: 4242, State: Running, Queue: &heavyqueue.Status{}, done: make(chan struct{})}
	plain := &Job{ID: "plain", PID: 4343, State: Running, done: make(chan struct{})}
	m.jobs[j.ID], m.jobs[plain.ID] = j, plain
	t0 := time.Now()

	m.memoryPause(plain, t0)
	m.memoryPause(j, t0)
	if len(sent) != 1 || sent[0] != syscall.SIGSTOP || !strings.Contains(j.Paused, "low memory, 100 MiB available") {
		t.Fatalf("heavy job must freeze alone: %v %q", sent, j.Paused)
	}
	if s, _ := m.Get("heavy"); !strings.Contains(s.Paused, "resumes above") {
		t.Fatalf("job_status must show the pause: %+v", s)
	}
	if !j.lastActive.Equal(t0) {
		t.Fatal("a frozen job must count as active (no stall kill)")
	}

	avail = 500 << 20
	m.memoryPause(j, t0.Add(time.Second)) // inside the check gap: still frozen
	m.memoryPause(j, t0.Add(10*time.Second))
	if j.Paused != "" || sent[len(sent)-1] != syscall.SIGCONT || j.PausedSeconds != 10 {
		t.Fatalf("memory back must resume: %v %q %d", sent, j.Paused, j.PausedSeconds)
	}

	// Bounded: memory stays low, the job resumes after maxPause and is not
	// frozen again during the cooldown.
	avail = 100 << 20
	t1 := t0.Add(time.Minute)
	m.memoryPause(j, t1)
	m.memoryPause(j, t1.Add(maxPause))
	if j.Paused != "" {
		t.Fatal("pause must end after maxPause")
	}
	m.memoryPause(j, t1.Add(maxPause+time.Minute))
	if j.Paused != "" {
		t.Fatal("cooldown must keep the job running")
	}
	m.memoryPause(j, t1.Add(maxPause+pauseCooldown+time.Minute))
	if j.Paused == "" {
		t.Fatal("after cooldown low memory pauses again")
	}
}
