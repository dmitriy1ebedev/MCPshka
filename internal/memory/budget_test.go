package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A statement killed by its own budget is marked errBudget (read retries
// it); one cancelled by the caller is not.
func TestExecBudgetIsDistinctFromCallerCancel(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.execFor(context.Background(), time.Nanosecond, "SELECT 1;"); !errors.Is(err, errBudget) {
		t.Fatal("own budget not marked", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.read(ctx, "SELECT 1;"); err == nil || errors.Is(err, errBudget) {
		t.Fatal("caller cancel retried or marked", err)
	}
	if rows, err := s.read(context.Background(), "SELECT 1 AS one;"); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
}
