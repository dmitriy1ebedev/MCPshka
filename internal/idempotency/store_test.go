package idempotency

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLookupMissingPreservesNotFound(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "idem"))
	if _, err := s.Lookup("absent"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing receipt must preserve not-found identity: %v", err)
	}
}

func TestBeginIsAtomicAndReplayable(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "idem"))
	fp, err := Fingerprint("write", map[string]any{"path": "/x", "value": 1})
	if err != nil {
		t.Fatal(err)
	}

	const n = 16
	var wg sync.WaitGroup
	results := make(chan BeginResult, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := store.Begin("same-key", "write", fp)
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	fresh := 0
	for got := range results {
		if got.Fresh {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh=%d want 1", fresh)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected begin errors: %d", len(errs))
	}

	want := struct {
		Value string `json:"value"`
	}{Value: "ok"}
	if err := store.Complete("same-key", "write", fp, want); err != nil {
		t.Fatal(err)
	}
	var replay struct {
		Value string `json:"value"`
	}
	state, err := store.ReadResult("same-key", "write", fp, &replay)
	if err != nil {
		t.Fatal(err)
	}
	if state != Completed || replay.Value != "ok" {
		t.Fatalf("state=%s replay=%+v", state, replay)
	}
}

func TestPendingSurvivesNewStoreInstance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "idem")
	fp, err := Fingerprint("exec", []string{"touch", "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := New(dir).Begin("k1", "exec", fp)
	if err != nil || !first.Fresh {
		t.Fatalf("begin=%+v err=%v", first, err)
	}
	second, err := New(dir).Begin("k1", "exec", fp)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fresh || second.Entry.State != Pending {
		t.Fatalf("unexpected replay: %+v", second)
	}
	status, err := New(dir).Lookup("k1")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != Pending || status.Operation != "exec" {
		t.Fatalf("status=%+v", status)
	}
}

func TestKeyConflictRejected(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "idem"))
	fp1, _ := Fingerprint("write", map[string]int{"v": 1})
	fp2, _ := Fingerprint("write", map[string]int{"v": 2})
	if _, err := store.Begin("key", "write", fp1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin("key", "write", fp2); !errors.Is(err, ErrConflict) {
		t.Fatalf("err=%v want ErrConflict", err)
	}
}

func TestFailedResultIsDurable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "idem")
	store := New(dir)
	fp, _ := Fingerprint("write", "x")
	if _, err := store.Begin("key", "write", fp); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail("key", "write", fp, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	status, err := New(dir).Lookup("key")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != Failed || status.Error != "boom" {
		t.Fatalf("status=%+v", status)
	}
	var out map[string]any
	state, err := New(dir).ReadResult("key", "write", fp, &out)
	if err != nil {
		t.Fatal(err)
	}
	if state != Failed {
		t.Fatalf("state=%s", state)
	}
}
