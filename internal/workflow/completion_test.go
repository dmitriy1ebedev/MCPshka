package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompletionOutboxSurvivesColdRestartWithoutReplay(t *testing.T) {
	e, m, root, sha := canary(t, "")
	lost := errors.New("post-step reply lost")
	e.OnTerminal = func(string) error { return lost }
	if _, err := e.Start(root, "release", "outbox-run", sha, "P7"); err != nil {
		t.Fatal(err)
	}
	var terminal Run
	deadline := time.Now().Add(30 * time.Second)
	for {
		r, err := e.Tick("outbox-run")
		if r.State == "done" {
			if !errors.Is(err, lost) {
				t.Fatal("lost callback not visible", err)
			}
			terminal = r
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatal("workflow failed", r, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	before, _ := os.ReadFile(filepath.Join(root, "events"))
	if _, err := e.db.Get(completionPrefix + terminal.ID); err != nil {
		t.Fatal("completion intent lost", err)
	}
	cold := New(m)
	calls := 0
	cold.OnTerminal = func(id string) error {
		calls++
		if id != terminal.ID {
			t.Fatal("changed ID", id)
		}
		return nil
	}
	if err := cold.Maintain(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("completion not redelivered", calls)
	}
	if _, err := e.db.Get(completionPrefix + terminal.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("delivered intent retained", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "events"))
	if string(before) != string(after) {
		t.Fatal("completion recovery replayed a job")
	}
}

func TestTerminalCallbackObservesReleasedWriterClaim(t *testing.T) {
	e, _, root, sha := canary(t, "")
	calls := 0
	e.OnTerminal = func(id string) error {
		calls++
		if id != "mission-run" {
			t.Fatal("changed workflow identity", id)
		}
		claim, err := e.ProjectClaim(root)
		if err != nil || claim != nil {
			t.Fatal("mission callback ran before writer release", claim, err)
		}
		return nil
	}
	if _, err := e.Start(root, "release", "mission-run", sha, "P7"); err != nil {
		t.Fatal(err)
	}
	if done := finish(t, e, "mission-run"); done.State != "done" || calls != 1 {
		t.Fatal(done, calls)
	}
	// A lost post-step reply is reconciled with the original workflow identity.
	if _, err := e.Tick("mission-run"); err != nil || calls != 2 {
		t.Fatal("terminal recovery lost callback", err, calls)
	}
}
