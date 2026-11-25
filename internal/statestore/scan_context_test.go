package statestore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestScanContextHonorsCallerDeadline(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutIfAbsent("p/a", []byte("1")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	visited := 0
	err = db.ScanContext(ctx, "p/", func(string, []byte) error { visited++; return nil })
	if !errors.Is(err, context.Canceled) || visited != 0 {
		t.Fatalf("canceled scan: err=%v visited=%d", err, visited)
	}
	stop := errors.New("stop")
	err = db.ScanContext(context.Background(), "p/", func(k string, v []byte) error {
		if k != "p/a" || string(v) != "1" {
			t.Fatalf("record %q=%q", k, v)
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("visitor error lost: %v", err)
	}
}
