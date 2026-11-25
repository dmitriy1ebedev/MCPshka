package integrationqueue

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParallelProducersAndSerialIntegrator(t *testing.T) {
	q := new(Queue)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("task-%02d", i)
			commit := strings.Repeat("a", 39) + string("0123456789abcdef"[i%16])
			if err := q.Enqueue(id, "chatgpt/task-"+id, commit); err != nil {
				t.Error(err)
				return
			}
			if err := q.Validate(id, commit); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if len(q.Snapshot()) != 30 {
		t.Fatal("missing producers")
	}
	item, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.Claim(); err == nil {
		t.Fatal("concurrent integrator admitted")
	}
	if err = q.Complete(item.ID, item.Commit, true); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Claim(); err != nil {
		t.Fatal(err)
	}
}
func TestFailClosedTransitions(t *testing.T) {
	q := new(Queue)
	commit := strings.Repeat("a", 40)
	if err := q.Enqueue("id", "branch", commit); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue("id", "branch", commit); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue("id", "other", commit); err == nil {
		t.Fatal("id conflict accepted")
	}
	if err := q.Validate("id", strings.Repeat("b", 40)); err == nil {
		t.Fatal("wrong commit accepted")
	}
	if _, err := q.Claim(); err == nil {
		t.Fatal("unvalidated claim accepted")
	}
	if err := q.Validate("id", commit); err != nil {
		t.Fatal(err)
	}
	item, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Complete(item.ID, strings.Repeat("b", 40), true); err == nil {
		t.Fatal("wrong completion accepted")
	}
	if err = q.Complete(item.ID, item.Commit, false); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Claim(); err == nil {
		t.Fatal("failed item claimed")
	}
}
func TestInvalidBranchAndCommit(t *testing.T) {
	q := new(Queue)
	for _, b := range []string{"../evil", "-flag", "branch with spaces", "a/../b", "a//b", "a.lock", "main", "HEAD", "a/"} {
		if err := q.Enqueue("x", b, strings.Repeat("a", 40)); err == nil {
			t.Fatalf("accepted %q", b)
		}
	}
	if err := q.Enqueue("x", "valid", "HEAD"); err == nil {
		t.Fatal("symbolic commit accepted")
	}
}

func TestConcurrentQueueAdmissionNeverExceedsDurableCapacity(t *testing.T) {
	q := new(Queue)
	commit := strings.Repeat("a", 40)
	for i := 0; i < 9990; i++ {
		q.items = append(q.items, Item{ID: fmt.Sprintf("seed-%d", i), Branch: "feature/seed", Commit: commit, State: Pending})
	}
	var wg sync.WaitGroup
	results := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- q.Enqueue(fmt.Sprintf("new-%d", i), "feature/new", commit)
		}(i)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 10 || len(q.Snapshot()) != 10000 {
		t.Fatalf("capacity lost under concurrency: accepted=%d items=%d", accepted, len(q.Snapshot()))
	}
	if err := q.Enqueue("seed-0", "feature/seed", commit); err != nil {
		t.Fatalf("transport retry rejected at capacity: %v", err)
	}
	if err := q.Save(filepath.Join(t.TempDir(), "queue.json")); err != nil {
		t.Fatalf("admitted queue cannot be persisted: %v", err)
	}
}

func TestUnboundedQueueIdentityRejectedBeforeAdmission(t *testing.T) {
	q := new(Queue)
	for _, id := range []string{strings.Repeat("a", 129), "../unsafe", "line\nfeed"} {
		if err := q.Enqueue(id, "feature/test", strings.Repeat("a", 40)); err == nil {
			t.Errorf("unbounded or unsafe ID admitted: %q", id)
		}
	}
	if len(q.Snapshot()) != 0 {
		t.Fatal("invalid admission mutated queue")
	}
}
