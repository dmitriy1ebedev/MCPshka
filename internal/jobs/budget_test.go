package jobs

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A job that shows no output, CPU or writes for its stall window ends long
// before its wall budget, with a reason that says so.
func TestStalledJobEndsEarly(t *testing.T) {
	m, work := testManager(t)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	j, err := m.StartWithOptions(sleep, []string{"30"}, work, StartOptions{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.jobs[j.ID].idle = 300 * time.Millisecond
	m.mu.Unlock()
	s := waitForTerminal(t, m, j.ID)
	if s.State != Failed || !strings.Contains(s.Error, "stalled") {
		t.Fatalf("want stalled failure, got %s %q", s.State, s.Error)
	}
}

// History raises a too-short request and the explanation names it.
func TestBudgetUsesHistory(t *testing.T) {
	m, work := testManager(t)
	now := time.Now().UTC()
	m.mu.Lock()
	m.jobs["h1"] = &Job{ID: "h1", Command: "/bin/true", Workdir: work, State: Done, StartedAt: now.Add(-40 * time.Minute), FinishedAt: now.Add(-10 * time.Minute)}
	m.mu.Unlock()
	d := m.Budget("/bin/true", nil, work, 10*time.Minute)
	if d.Source != "raised" || d.Wall < 62*time.Minute || !strings.Contains(d.String(), "поднят по истории") {
		t.Fatalf("%s %s %s", d.Source, d.Wall, d)
	}
	if d := m.Budget("/bin/true", []string{"other"}, work, 10*time.Minute); d.Source != "requested" {
		t.Fatalf("other args share history: %s", d.Source)
	}
}

// A job started by the lease holder renews the lease while it runs.
func TestHolderJobKeepsLeaseAlive(t *testing.T) {
	m, work := testManager(t)
	old := HolderKeepaliveInterval
	HolderKeepaliveInterval = 20 * time.Millisecond
	t.Cleanup(func() { HolderKeepaliveInterval = old })
	beats := make(chan string, 16)
	m.SetHolderGuard(func(string, string) error { return nil })
	m.SetHolderKeepalive(func(path, token string) error { beats <- token; return nil })
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	j, err := m.StartWithOptions(sleep, []string{"1"}, work, StartOptions{HolderToken: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case tok := <-beats:
		if tok != "tok-1" {
			t.Fatal(tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no keepalive while the job ran")
	}
	waitForTerminal(t, m, j.ID)
}
