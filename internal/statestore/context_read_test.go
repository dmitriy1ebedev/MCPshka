package statestore

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGetContextRespectsCancelledCaller(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Put("proof", []byte("visible")); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = db.GetContext(ctx, "proof"); !errors.Is(e, context.Canceled) {
		t.Fatalf("caller deadline lost: %v", e)
	}
}

func TestGetContextKillsBlockedSQLiteRead(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "locked.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.run("PRAGMA journal_mode=DELETE;"); e != nil {
		t.Fatal(e)
	}
	helper := exec.Command("python3", "-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute('BEGIN EXCLUSIVE'); print('locked',flush=True); sys.stdin.read()", db.file)
	input, e := helper.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	output, e := helper.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = helper.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { input.Close(); helper.Process.Kill(); helper.Wait() }()
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(output); ready <- scanner.Scan() && scanner.Text() == "locked" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("fixture lock not confirmed")
		}
	case <-time.After(time.Second):
		t.Fatal("lock helper timed out")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e = db.GetContext(ctx, "absent")
	if !errors.Is(e, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("SQLite ignored caller budget: %v", e)
	}
}
