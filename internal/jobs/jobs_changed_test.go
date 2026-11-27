package jobs

import (
	"testing"
	"time"
)

// MCP-273: a running job gives a signal that closes when it ends.
func TestChangedClosesWhenTheJobEnds(t *testing.T) {
	m, work := testManager(t)
	j, err := m.Start("sleep", []string{"0.2"}, work)
	if err != nil {
		t.Fatal(err)
	}
	ch := m.Changed(j.ID)
	if ch == nil {
		t.Fatal("a local running job has no change signal")
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("signal not closed when the job ended")
	}
	s, err := m.Get(j.ID)
	if err != nil || s.State != Done {
		t.Fatalf("state after the signal: %v %v", s.State, err)
	}
	if m.Changed("missing") != nil {
		t.Fatal("an unknown job must fall back to polling")
	}
}
