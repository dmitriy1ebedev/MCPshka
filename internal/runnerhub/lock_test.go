package runnerhub

import (
	"strings"
	"sync"
	"testing"
)

func TestSeparateHubsDoNotLoseConcurrentEnqueues(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for n := range 24 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			h := New(dir)
			j, _ := Plan("/root/repos/demo", strings.Repeat("a", 40), "go-test", nil, 3600)
			j.ID = NewJobID()
			if _, err := h.Enqueue(j); err != nil {
				t.Error(err)
			}
		}(n)
	}
	wg.Wait()
	_, js, err := New(dir).Snapshot()
	if err != nil || len(js) != 24 {
		t.Fatal(len(js), err)
	}
}
func TestEnqueueDeduplicatesAtomicallyAndPreservesLiveJobs(t *testing.T) {
	h := New(t.TempDir())
	var first Job
	for n := range maxJobs {
		j, _ := Plan("/root/repos/demo", strings.Repeat("a", 40), "go-test", nil, 3600)
		j.ID = NewJobID()
		if n == 0 {
			j.KeyHash = KeyDigest("same")
		}
		got, err := h.Enqueue(j)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			first = got
		}
	}
	retry := first
	retry.ID = NewJobID()
	got, err := h.Enqueue(retry)
	if err != nil || got.ID != first.ID {
		t.Fatal("duplicate", got, err)
	}
	retry.KeyHash = KeyDigest("different")
	if _, err := h.Enqueue(retry); err == nil {
		t.Fatal("evicted live job")
	}
}
