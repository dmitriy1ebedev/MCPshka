package mcpserver

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMutationGateParallelAndExclusiveFinish(t *testing.T) {
	var g mutationGate
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, e := g.enter(ctx, false)
	if e != nil {
		t.Fatal(e)
	}
	defer a()
	b, e := g.enter(ctx, false)
	if e != nil {
		t.Fatal(e)
	}
	wait, c := context.WithTimeout(ctx, 20*time.Millisecond)
	_, e = g.enter(wait, true)
	c()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("yield did not wait for mutation: %v", e)
	}
	b()
	a()
	finish, e := g.enter(ctx, true)
	if e != nil {
		t.Fatal(e)
	}
	wait, c = context.WithTimeout(ctx, 20*time.Millisecond)
	_, e = g.enter(wait, false)
	c()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("mutation admitted during yield: %v", e)
	}
	finish()
	next, e := g.enter(ctx, false)
	if e != nil {
		t.Fatalf("admission not reopened: %v", e)
	}
	next()
}
func TestMutationGateCancelledYieldDoesNotBlockReaders(t *testing.T) {
	var g mutationGate
	ctx := context.Background()
	reader, e := g.enter(ctx, false)
	if e != nil {
		t.Fatal(e)
	}
	defer reader()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = g.enter(cancelled, true); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancelled request admitted: %v", e)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	second, e := g.enter(bounded, false)
	if e != nil {
		t.Fatalf("cancelled exclusive waiter stranded admission: %v", e)
	}
	second()
}
