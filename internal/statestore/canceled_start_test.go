package statestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCanceledCoprocessDoesNotInitializeOrRunSQL(t *testing.T) {
	file := filepath.Join(t.TempDir(), "invalid.db")
	if err := os.WriteFile(file, []byte("invalid SQLite"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &coprocess{file: file}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.run(ctx, "SELECT 1;")
	if !errors.Is(err, context.Canceled) || c.calls != 0 || c.cmd != nil {
		t.Fatalf("canceled initialization lost cause or executed: %v calls=%d alive=%v", err, c.calls, c.cmd != nil)
	}
}
