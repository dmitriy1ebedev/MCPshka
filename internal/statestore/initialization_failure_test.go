package statestore

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBudgetInitializationFailureDoesNotExecuteSQL(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "locked.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.run("PRAGMA journal_mode=DELETE;"); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command("python3", "-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute('BEGIN EXCLUSIVE'); print('locked',flush=True); sys.stdin.read(); c.rollback()", db.file)
	input, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); helper.Process.Kill(); helper.Wait() }()
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(output); ready <- scanner.Scan() && scanner.Text() == "locked" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("lock fixture failed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lock fixture timed out")
	}
	// The CLI's -cmd pragma times out first; stdin must never run afterward.
	released := make(chan struct{})
	go func() { time.Sleep(4500 * time.Millisecond); input.Close(); close(released) }()
	_, err = db.runOnceBudget(context.Background(), "INSERT INTO records VALUES ('unsafe', X'61', 'irrelevant');", 15*time.Second)
	<-released
	if err == nil {
		t.Fatal("locked initialization succeeded")
	}
	out, readErr := db.runOnce(context.Background(), "SELECT count(*) FROM records WHERE path='unsafe';")
	if readErr != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("SQL ran after failed initialization: %q %v (initial error %v)", out, readErr, err)
	}
}
