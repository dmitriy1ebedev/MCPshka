package statestore

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

func TestHealthReadOnlyOpenDoesNotCreateOrWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if _, err := OpenReadOnlyContext(context.Background(), path); err == nil {
		t.Fatal("missing authority opened")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read-only open created authority")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.PutIfAbsent("proof", []byte("visible")); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnlyContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ro.GetContext(context.Background(), "proof")
	if err != nil || string(raw) != "visible" {
		t.Fatal(string(raw), err)
	}
	if _, err := ro.PutIfAbsent("write", []byte("forbidden")); err == nil {
		t.Fatal("read-only handle wrote authority")
	}
}

func TestHealthReadOnlyOpenKillsBlockedSchemaRead(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "locked.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.run("PRAGMA journal_mode=DELETE;"); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command("python3", "-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute('BEGIN EXCLUSIVE'); print('locked',flush=True); sys.stdin.read()", db.file)
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
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = OpenReadOnlyContext(ctx, db.file)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("opening ignored health deadline: %v", err)
	}
}
