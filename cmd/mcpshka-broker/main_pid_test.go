package main

import (
	"os"
	"testing"
)

func TestCachedMainPIDRefreshesOnRestartOrReuse(t *testing.T) {
	lookups := 0
	pid, start := int32(100), "1"
	c := cachedMainPID(func() (int32, error) { lookups++; return pid, nil }, func(int32) (string, error) { return start, nil })
	for range 5 {
		if p, _ := c(); p != 100 {
			t.Fatal(p)
		}
	}
	if lookups != 1 {
		t.Fatalf("systemd asked %d times for a stable process", lookups)
	}
	start = "2" // same PID, different process: reused
	pid = 200
	if p, _ := c(); p != 200 || lookups != 2 {
		t.Fatal("reused PID accepted from cache", p, lookups)
	}
	if st, err := procStartTime(int32(os.Getpid())); err != nil || st == "" {
		t.Fatal(st, err)
	}
}
