package idempotency

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

func TestPruneRemovesOnlyExpiredFinishedAllowlistedReceipts(t *testing.T) {
	dir := t.TempDir()
	db, err := statestore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithDatabase(filepath.Join(dir, "idempotency"), db)
	finish := func(key, op string, fail bool) {
		t.Helper()
		if _, err := s.Begin(key, op, "fp"); err != nil {
			t.Fatal(err)
		}
		if fail {
			err = s.Fail(key, op, "fp", errors.New("x"))
		} else {
			err = s.Complete(key, op, "fp", map[string]string{"ok": "1"})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	finish("old-exec", "exec_start", false)
	finish("old-contract", "contract/fs_patch", true)
	finish("old-deploy", "project_deploy", false)
	if _, err := s.Begin("old-pending", "exec_start", "fp"); err != nil {
		t.Fatal(err)
	}
	if err := db.Put("idempotency/garbage.json", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(db, time.Now(), 100); err != nil || n != 0 {
		t.Fatalf("fresh receipts pruned: n=%d err=%v", n, err)
	}
	later := time.Now().Add(73 * time.Hour)
	if n, err := Prune(db, later, 1); err != nil || n != 1 {
		t.Fatalf("batch limit: n=%d err=%v", n, err)
	}
	if n, err := Prune(db, later, 100); err != nil || n != 1 {
		t.Fatalf("second batch: n=%d err=%v", n, err)
	}
	for _, key := range []string{"old-exec", "old-contract"} {
		if _, err := s.Lookup(key); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s kept: %v", key, err)
		}
	}
	for _, key := range []string{"old-deploy", "old-pending"} {
		if _, err := s.Lookup(key); err != nil {
			t.Fatalf("%s pruned: %v", key, err)
		}
	}
	if _, err := db.Get("idempotency/garbage.json"); err != nil {
		t.Fatal("invalid record touched", err)
	}
	if _, err := db.PruneFinished("idempotency/", statestore.PruneRule{States: []string{"completed"}, FinishedBefore: later, Limit: 1}); err == nil {
		t.Fatal("empty operation allowlist accepted")
	}
}
