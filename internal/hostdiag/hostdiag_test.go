package hostdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeHost(t *testing.T, avail, swapFree string, allocstall [2]string) {
	t.Helper()
	Proc, Cgroup = t.TempDir(), t.TempDir()
	w := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w(filepath.Join(Proc, "loadavg"), "0.5 0.4 0.3 1/100 1\n")
	w(filepath.Join(Proc, "meminfo"), "MemTotal: 1000000 kB\nMemAvailable: "+avail+" kB\nSwapTotal: 100 kB\nSwapFree: "+swapFree+" kB\n")
	w(filepath.Join(Proc, "vmstat"), "allocstall_normal "+allocstall[0]+"\noom_kill 0\n")
	w(filepath.Join(Proc, "sys/kernel/threads-max"), "1000\n")
	w(filepath.Join(Proc, "42/status"), "Name:\tsshd\nThreads:\t3\n")
	w(filepath.Join(Cgroup, "system.slice/ssh.service/pids.current"), "120\n")
	w(filepath.Join(Cgroup, "system.slice/ssh.service/pids.max"), "128\n")
}

func TestHealthyHostHasNoFault(t *testing.T) {
	fakeHost(t, "800000", "100", [2]string{"0", "0"})
	os.Remove(filepath.Join(Cgroup, "system.slice/ssh.service/pids.max"))
	r := Diagnose(t.Context(), "general", &Events{}, time.Millisecond)
	if r.Verdict != "NO_FAULT_FOUND" {
		t.Fatalf("%s %+v", r.Verdict, r.Facts)
	}
}

func TestPidLimitAndSSHDropUnderPressure(t *testing.T) {
	fakeHost(t, "50000", "0", [2]string{"0", "0"})
	ev := &Events{SSH: []string{"sshd: Timeout, client not responding"}}
	ev.Counts.SSHDrop = 1
	r := Diagnose(t.Context(), "ssh_drop", ev, time.Millisecond)
	if r.Verdict != "CAUSE_FOUND" || len(r.Inferences) < 3 {
		t.Fatalf("%s %+v", r.Verdict, r.Inferences)
	}
	want := map[string]bool{"tasks:ssh.service": false, "memory": false}
	for _, inf := range r.Inferences {
		for _, id := range inf.BasedOn {
			if _, ok := want[id]; ok {
				want[id] = true
			}
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("no inference based on %s: %+v", id, r.Inferences)
		}
	}
}

func TestMissingBrokerStaysUnknown(t *testing.T) {
	fakeHost(t, "800000", "100", [2]string{"0", "0"})
	os.Remove(filepath.Join(Cgroup, "system.slice/ssh.service/pids.max"))
	r := Diagnose(t.Context(), "ssh_drop", nil, time.Millisecond)
	if r.Verdict != "UNKNOWN" || len(r.MissingChecks) == 0 {
		t.Fatalf("%s %+v", r.Verdict, r.MissingChecks)
	}
}

func TestPidsRejectionNamesUnit(t *testing.T) {
	fakeHost(t, "800000", "100", [2]string{"0", "0"})
	os.Remove(filepath.Join(Cgroup, "system.slice/ssh.service/pids.max"))
	ev := &Events{Kernel: []string{"kernel: cgroup: fork rejected by pids controller in /system.slice/mcpshka-broker.service"}}
	ev.Counts.ForkFailed = 1
	r := Diagnose(t.Context(), "thread_eagain", ev, time.Millisecond)
	if r.Verdict != "CAUSE_FOUND" || len(r.Inferences) == 0 || !strings.Contains(r.Inferences[len(r.Inferences)-1].Claim, "mcpshka-broker.service") {
		t.Fatalf("%s %+v", r.Verdict, r.Inferences)
	}
}
