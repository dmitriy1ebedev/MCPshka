package mcpserver

import (
	"context"
	"testing"
	"time"
)

func TestExecutorSelfHealLoopReports(t *testing.T) {
	got := make(chan int, 4)
	stop := startExecutorSelfHeal(time.Millisecond, time.Millisecond, func(context.Context) ([]byte, error) {
		return []byte(`{"reconciled":2,"skipped":0}`), nil
	}, func(n int, err error) {
		if err == nil {
			got <- n
		}
	})
	defer stop()
	select {
	case n := <-got:
		if n != 2 {
			t.Fatalf("reconciled %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no pass")
	}
}
