package jobs

import (
	"strings"
	"testing"
	"time"
)

func TestInteractivePipeAndOwnership(t *testing.T) {
	m, dir := testManager(t)
	s, err := m.StartWithOptions("cat", nil, dir, StartOptions{InteractiveOwner: "one", OutputLimitBytes: 32, InteractiveIdle: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Cancel(s.ID)
	if err = m.SendInput(s.ID, "two", "bad"); err == nil {
		t.Fatal("foreign input accepted")
	}
	if err = m.SendInput(s.ID, "one", "hello\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		o, err := m.Read(s.ID, "", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(o.Text, "hello") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing echo")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(m.InteractiveSessions("two")) != 0 || len(m.InteractiveSessions("one")) != 1 {
		t.Fatal("session ownership")
	}
	terminal := waitForTerminal(t, m, s.ID)
	if terminal.State != Cancelled {
		t.Fatalf("idle session: %s", terminal.State)
	}
}
func TestInteractivePTY(t *testing.T) {
	m, dir := testManager(t)
	s, err := m.StartWithOptions("cat", nil, dir, StartOptions{InteractiveOwner: "one", PTY: true})
	if err != nil {
		t.Fatal(err)
	}
	// The job keeps writing into its TempDir until the process is reaped:
	// wait for the terminal state, or cleanup races with it under load.
	defer cancelAndWait(t, m, s.ID)
	if err = m.SendInput(s.ID, "one", "hello\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		o, _ := m.Read(s.ID, "", 0, 100)
		if strings.Contains(o.Text, "hello") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("missing PTY output")
}

func cancelAndWait(t *testing.T, m *Manager, id string) {
	t.Helper()
	m.Cancel(id)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if s, err := m.Get(id); err != nil || (s.State != Running && s.State != Cancelling && s.State != Queued) {
			return
		}
	}
	t.Error("job did not stop after cancel")
}
