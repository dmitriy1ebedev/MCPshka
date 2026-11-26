package jobs

import (
	"mcpshka/internal/policy"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeavyJobPriority(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		opts    StartOptions
		want    bool
	}{
		{"go", []string{"test", "./..."}, StartOptions{}, true},
		{"/usr/local/bin/go", []string{"build", "./..."}, StartOptions{}, true},
		{"go", []string{"version"}, StartOptions{}, false},
		{"printf", nil, StartOptions{}, false},
		{"make", []string{"test"}, StartOptions{LowPriority: true}, true},
		{"go", []string{"test"}, StartOptions{DisableLowPriority: true}, false},
	} {
		if got := heavyJobPriority(tc.command, tc.args, tc.opts); got != tc.want {
			t.Errorf("priority %s %v = %v", tc.command, tc.args, got)
		}
	}
	command, args := lowPriorityCommand("go", []string{"test", "./x"})
	if command != "/usr/bin/ionice" || !reflect.DeepEqual(args, []string{"-c", "3", "/usr/bin/nice", "-n", "10", "--", "go", "test", "./x"}) {
		t.Fatalf("bad priority argv: %s %v", command, args)
	}
	_, cmd := buildScopedCommand("priority", command, args, DefaultMemoryLimit, DefaultCPUQuotaPercent, DefaultProcessLimit)
	if !reflect.DeepEqual(cmd.Args[len(cmd.Args)-len(args)-1:], append([]string{command}, args...)) {
		t.Fatalf("priority wrapper outside scope: %v", cmd.Args)
	}
}

func TestHeavyJobEffectivePriority(t *testing.T) {
	m, work := testManager(t)
	defer m.Close(time.Second)
	started, err := m.StartWithOptions("sleep", []string{"3"}, work, StartOptions{LowPriority: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		nice, err := exec.Command("ps", "-o", "ni=", "-p", strconv.Itoa(started.PID)).Output()
		if err != nil {
			t.Fatal(err)
		}
		io, err := exec.Command("/usr/bin/ionice", "-p", strconv.Itoa(started.PID)).Output()
		if err != nil {
			t.Fatal(err)
		}
		level, parseErr := strconv.Atoi(strings.TrimSpace(string(nice)))
		if parseErr == nil && level >= 10 && strings.TrimSpace(string(io)) == "idle" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not acquire nice>=10 and idle IO")
}

func TestHeavyJobPreservesExecutableResolution(t *testing.T) {
	m, work := testManager(t)
	defer m.Close(time.Second)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf selected-toolchain"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	started, err := m.StartWithOptions("go", []string{"version"}, work, StartOptions{LowPriority: true})
	if err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, started.ID)
	out, err := m.Read(started.ID, "stdout", 0, 128)
	if err != nil || s.State != Done || out.Text != "selected-toolchain" {
		t.Fatalf("toolchain changed: %+v %+v %v", s, out, err)
	}
}

func TestHeavyJobsQueueBeforeExecutionBudget(t *testing.T) {
	m, work := testManager(t)
	defer m.Close(time.Second)
	var busy atomic.Bool
	busy.Store(true)
	m.heavyQueue.Ready = func() (bool, string) { return !busy.Load(), "host load" }
	first, err := m.StartWithOptions("sleep", []string{"0.1"}, work, StartOptions{LowPriority: true, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.StartWithOptions("true", nil, work, StartOptions{LowPriority: true})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	a, _ := m.Get(first.ID)
	b, _ := m.Get(second.ID)
	if a.State != Queued || b.State != Queued || a.Queue == nil || a.Queue.Reason != "host load" || b.Queue.Position != 2 {
		t.Fatalf("queue not visible: %+v %+v", a, b)
	}
	busy.Store(false)
	if s := waitForTerminal(t, m, first.ID); s.State != TimedOut {
		t.Fatalf("execution budget missing: %+v", s)
	}
	if s := waitForTerminal(t, m, second.ID); s.State != Done {
		t.Fatalf("queue did not drain: %+v", s)
	}
	if err := m.Close(time.Second); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(m.heavyQueue.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".ticket") {
			t.Fatal("manager returned before slot release", file.Name())
		}
	}
}
func TestCancelQueuedHeavyJobNeverExecutes(t *testing.T) {
	m, work := testManager(t)
	defer m.Close(time.Second)
	m.heavyQueue.Ready = func() (bool, string) { return false, "load" }
	j, err := m.StartWithOptions("printf", []string{"must-not-run"}, work, StartOptions{LowPriority: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, j.ID)
	out, err := m.Read(j.ID, "stdout", 0, 128)
	if s.State != Cancelled || err != nil || out.Text != "" {
		t.Fatalf("%+v %+v %v", s, out, err)
	}
}

func TestStandaloneManagerQueueNeedsOnlyItsStateDirectory(t *testing.T) {
	root, work := t.TempDir(), t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{work}, DeniedCommands: map[string]struct{}{}}
	m, err := New(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	j, err := m.StartWithOptions("true", nil, work, StartOptions{LowPriority: true})
	if err != nil {
		t.Fatal(err)
	}
	if s := waitForTerminal(t, m, j.ID); s.State != Done {
		t.Fatalf("isolated manager: %+v", s)
	}
	if err := m.Close(time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".heavy-queue", ".lock")); err != nil {
		t.Fatalf("queue escaped manager state: %v", err)
	}
}

func TestRefusedAdmissionKeepsItsReason(t *testing.T) {
	m, work := testManager(t)
	defer m.Close(time.Second)
	m.heavyQueue.Ready = func() (bool, string) { return false, "load" }
	j, err := m.StartWithOptions("printf", []string{"must-not-run"}, work, StartOptions{LowPriority: true})
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(m.heavyQueue.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".ticket") {
			os.Remove(filepath.Join(m.heavyQueue.Dir, file.Name()))
		}
	}
	s := waitForTerminal(t, m, j.ID)
	if s.State != Failed || !strings.HasPrefix(s.Error, "heavy queue admission: ") {
		t.Fatalf("admission reason lost: %+v", s)
	}
}
