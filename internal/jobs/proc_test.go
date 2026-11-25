package jobs

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseProcStatHandlesSpacesInCommand(t *testing.T) {
	fields := make([]string, 22)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[1] = "1"
	fields[2] = "123"
	fields[11] = "40"
	fields[12] = "2"
	fields[21] = "10"
	raw := "123 (worker name) " + strings.Join(fields, " ")

	got, err := parseProcStat(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.pid != 123 || got.pgrp != 123 || got.state != "S" || got.utime != 40 || got.stime != 2 || got.rssPages != 10 {
		t.Fatalf("unexpected stat: %+v", got)
	}
}

func TestGetReportsRunningProcessGroupObservation(t *testing.T) {
	m, work := testManager(t)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	started, err := m.StartWithOptions(sleep, []string{"5"}, work, StartOptions{
		Timeout:          10 * time.Second,
		OutputLimitBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	done := m.jobs[started.ID].done
	m.mu.RUnlock()
	defer func() {
		_, _ = m.Cancel(started.ID)
		// Cancel is asynchronous; wait for persistence before TempDir cleanup.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("cancelled job did not finish persisting")
		}
	}()

	var got Snapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err = m.Get(started.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Process != nil && got.Process.Alive && got.Process.ProcessCount > 0 && got.Process.LeaderState != "" && got.Process.RSSBytes > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Process == nil {
		t.Fatal("missing process observation")
	}
	if !got.Process.Alive || got.Process.ProcessCount < 1 {
		t.Fatalf("process group not observed: %+v", got.Process)
	}
	if got.Process.LeaderState == "" {
		t.Fatalf("leader state missing: %+v", got.Process)
	}
	if got.Process.ObservedAt.IsZero() {
		t.Fatalf("observed_at missing: %+v", got.Process)
	}
	if got.Process.RSSBytes == 0 {
		t.Fatalf("rss not observed: %+v", got.Process)
	}
}
