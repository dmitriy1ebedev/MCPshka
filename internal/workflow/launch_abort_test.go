package workflow

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// An actual SQLite writer lock makes the post-admission failure receipt time
// out. Losing that write must not strand a live supervisor indefinitely.
func TestLaunchAbortSurvivesFailedSnapshotWrite(t *testing.T) {
	e, m, root, sha := canary(t, "")
	r, err := e.Start(root, "release", "abort-write-canary", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	lock := exec.CommandContext(ctx, "python3", "-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute('BEGIN IMMEDIATE'); print('locked',flush=True); sys.stdin.read(1); c.rollback()", filepath.Join(filepath.Dir(m.StateDir()), "state.db"))
	input, err := lock.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := lock.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	started := false
	m.SetLeaseGuard(func(string, string) error {
		if !started {
			if err := lock.Start(); err != nil {
				return err
			}
			started = true
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil || line != "locked\n" {
				return errors.New("SQLite fault fixture unavailable")
			}
		}
		return errors.New("controlled pre-launch admission rejection")
	})
	t.Cleanup(func() {
		input.Close()
		if started {
			lock.Wait()
		}
	})
	if _, err = e.Tick(r.ID); err == nil {
		t.Fatal("locked abort snapshot unexpectedly persisted")
	}
	input.Close()
	if err = lock.Wait(); err != nil {
		t.Fatal(err)
	}
	started = false
	m.SetLeaseGuard(e.Guard)
	cold := New(m)
	recovered, err := cold.Tick(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.LaunchAborted {
		t.Fatalf("known launch abort lost after snapshot failure: %+v", recovered)
	}
	done := finish(t, cold, r.ID)
	if done.State != "rolled_back" {
		t.Fatalf("failed admission did not settle: %+v", done)
	}
	events, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if string(events) != "rollback\nfinalizer\n" {
		t.Fatalf("original stage replayed: %q", events)
	}
}
