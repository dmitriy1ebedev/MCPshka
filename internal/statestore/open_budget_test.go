package statestore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteStateHasHeadroomInBothExecutionModes(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	process := coprocessFor(db.file, false)
	t.Cleanup(process.stop)
	for name, run := range map[string]func(context.Context, string) ([]byte, error){
		"one-shot":  db.runOnce,
		"coprocess": process.run,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := run(t.Context(), "PRAGMA max_page_count;")
			if err != nil || string(out) != "65536\n" {
				t.Fatalf("SQLite page limit: %q, %v", out, err)
			}
		})
	}
}

func TestSchemaBudgetDoesNotExtendOrdinaryOperations(t *testing.T) {
	if operationTimeout != 5*time.Second || openTimeout != 30*time.Second {
		t.Fatal("unexpected SQLite budgets")
	}
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	// The same SQLite path must obey both the operation budget and an earlier caller deadline.
	if _, err := db.runOnceBudget(context.Background(), "SELECT 1;", time.Nanosecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("operation budget: %v", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := db.runOnceBudget(ctx, "SELECT 1;", openTimeout); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller budget: %v", err)
	}
	out, err := db.runOnceBudget(context.Background(), "SELECT 1;", openTimeout)
	if err != nil || string(out) != "1\n" {
		t.Fatalf("schema query: %q %v", out, err)
	}
	if _, err := db.PutIfAbsent("budget/check", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(db.file); err != nil {
		t.Fatalf("reopen: %v", err)
	}
}
