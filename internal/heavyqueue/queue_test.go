package heavyqueue

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueueSerializesAndReportsLoad(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	q := Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return !busy.Load(), "load1 exceeds 1.5 x CPU" }}
	a, e := q.Enqueue("build")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := q.Enqueue("test")
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	s, e := b.Status()
	if e != nil || s.Position != 2 || s.State != "QUEUED" {
		t.Fatalf("%+v %v", s, e)
	}
	s, e = a.Status()
	if e != nil || s.Reason != "load1 exceeds 1.5 x CPU" {
		t.Fatalf("%+v %v", s, e)
	}
	busy.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = a.Wait(ctx, nil); e != nil {
		t.Fatal(e)
	}
	s, e = b.Status()
	if e != nil || s.State != "QUEUED" || s.Position != 1 {
		t.Fatalf("second started while first runs: %+v %v", s, e)
	}
	a.Close()
	if e = b.Wait(ctx, nil); e != nil {
		t.Fatal(e)
	}
}

func TestQueueCancelledAndAbandonedTicket(t *testing.T) {
	q := Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return false, "load" }}
	a, e := q.Enqueue("cancelled")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = a.Wait(ctx, nil); e != context.Canceled {
		t.Fatal(e)
	}
	a.Close()
	b, e := q.Enqueue("next")
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	s, e := b.Status()
	if e != nil || s.Position != 1 {
		t.Fatalf("cancelled ticket blocks queue: %+v %v", s, e)
	}
}

func TestAgentDelegatedWorkUsesItsExistingSlot(t *testing.T) {
	q := Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	agent, err := q.EnqueueScoped("agent:a", "/repo")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = agent.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	foreign, err := q.EnqueueScoped("build:foreign", "/other")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	child, err := q.EnqueueScoped("job:test", "/repo")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if err = child.Wait(ctx, nil); err != nil {
		t.Fatal("agent deadlocked on its own test", err)
	}
	second, err := q.EnqueueScoped("job:second", "/repo")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if s, err := second.Status(); err != nil || s.State != "QUEUED" {
		t.Fatalf("two delegated heavy tasks: %+v %v", s, err)
	}
	child.Close()
	if err = second.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if s, err := foreign.Status(); err != nil || s.State != "QUEUED" {
		t.Fatalf("foreign work overlaps the agent's running child: %+v %v", s, err)
	}
	second.Close()
	// An idle agent (no heavy child running) no longer holds the host for
	// its whole session (BUG-20261024-97).
	if err = foreign.Wait(ctx, nil); err != nil {
		t.Fatal("foreign work blocked by an idle agent", err)
	}
}
func TestQueueDropsTicketOnlyAfterKernelLockReleased(t *testing.T) {
	q := Queue{Dir: t.TempDir(), Ready: func() (bool, string) { return true, "" }}
	a, err := q.Enqueue("crashed")
	if err != nil {
		t.Fatal(err)
	}
	a.File.Close() // Model process death without graceful unlink.
	b, err := q.Enqueue("next")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if s, err := b.Status(); err != nil || s.Position != 1 {
		t.Fatalf("orphan blocks queue: %+v %v", s, err)
	}
}

func TestQueueReportsBlockingWorkAndWaitDuration(t *testing.T) {
	q := Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	head, err := q.Enqueue("build:diag")
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := head.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	waiting, err := q.Enqueue("test:lane")
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Close()
	last, err := q.Enqueue("test:mcpshka")
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	state, err := waiting.Status()
	if err != nil || state.HeadLabel != "build:diag" || state.AheadCount != 1 || state.QueuedAt.IsZero() || state.WaitSeconds < 0 {
		t.Fatalf("missing running queue head evidence: %+v %v", state, err)
	}
	state, err = last.Status()
	if err != nil || state.HeadLabel != "build:diag" || state.AheadCount != 2 {
		t.Fatalf("missing full FIFO evidence: %+v %v", state, err)
	}
	if err := head.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = last.Status()
	if err != nil || state.HeadLabel != "test:lane" || state.AheadCount != 1 {
		t.Fatalf("queued head not reported after worker completion: %+v %v", state, err)
	}
	if err := waiting.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := waiting.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = last.Status()
	if err != nil || state.AheadCount != 0 || state.HeadLabel != "" {
		t.Fatalf("stale queue head after release: %+v %v", state, err)
	}
}

func TestClosingTicketDoesNotRecreateRetiredStorage(t *testing.T) {
	q := Queue{Dir: filepath.Join(t.TempDir(), "retired"), Ready: func() (bool, string) { return true, "" }}
	ticket, err := q.Enqueue("finished")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(q.Dir); err != nil {
		t.Fatal(err)
	}
	if err := ticket.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(q.Dir); !os.IsNotExist(err) {
		t.Fatalf("retired queue recreated: %v", err)
	}
}
